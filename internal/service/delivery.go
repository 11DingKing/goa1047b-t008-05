package service

import (
	"strconv"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/store"
)

// DeliveryService handles cargo collection, temperature data collection,
// loading manifest verification, destination-port delivery confirmation,
// and the 24-hour auto-release rule.
type DeliveryService struct {
	store    *store.Store
	notifier Notifier
}

// NewDeliveryService creates a DeliveryService.
func NewDeliveryService(s *store.Store, n Notifier) *DeliveryService {
	return &DeliveryService{store: s, notifier: n}
}

// RecordCargoArrival records that a container has arrived at the loading-port yard.
func (s *DeliveryService) RecordCargoArrival(containerID string, arrivedAt time.Time) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	c, ok := s.store.GetContainer(containerID)
	if !ok {
		return domain.ErrContainerNotFound
	}
	if err := c.RecordArrival(arrivedAt); err != nil {
		return err
	}
	s.store.SaveContainer(c)

	slotID := ""
	s.store.AppendEvent(domain.CargoArrivedEvent{
		ContainerID: containerID,
		SlotID:      slotID,
		OccurredAt:  arrivedAt,
	})
	return nil
}

// RecordTemperature records a temperature reading for a temperature-controlled container.
// An alert event is emitted when the reading is outside the 15–25 °C range.
func (s *DeliveryService) RecordTemperature(containerID string, ts time.Time, temp float64) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	c, ok := s.store.GetContainer(containerID)
	if !ok {
		return domain.ErrContainerNotFound
	}
	if err := c.RecordTemperature(ts, temp); err != nil {
		return err
	}
	s.store.SaveContainer(c)

	if temp < domain.MinTempC || temp > domain.MaxTempC {
		s.store.AppendEvent(domain.TemperatureAlertEvent{
			ContainerID: containerID,
			Temperature: temp,
			OccurredAt:  ts,
		})
		if s.notifier != nil {
			s.notifier.Notify(c.OwnerID,
				"temperature alert for container "+containerID+": "+strconv.FormatFloat(temp, 'f', 1, 64)+"C")
		}
	}
	return nil
}

// VerifyLoadingManifest checks that all locked reservations on a slot have
// arrived containers with compliant temperatures, then transitions them to loaded.
// Returns the reservation IDs that were loaded.
func (s *DeliveryService) VerifyLoadingManifest(slotID string, now time.Time) ([]string, error) {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	slot, ok := s.store.GetSlot(slotID)
	if !ok {
		return nil, domain.ErrSlotNotFound
	}
	if slot.Status != domain.SlotStatusLocked && slot.Status != domain.SlotStatusFrozen {
		return nil, domain.ErrInvalidState
	}

	reservations := s.store.ReservationsBySlot(slotID)
	var loaded []string
	for _, r := range reservations {
		if r.Status != domain.ReservationLocked {
			continue
		}
		c, ok := s.store.GetContainer(r.ContainerID)
		if !ok {
			continue
		}
		if c.Status != domain.ContainerArrived {
			continue
		}
		if c.CargoType.IsTemperatureControlled() && !c.IsTemperatureCompliant() {
			continue
		}
		if err := c.Load(now); err != nil {
			continue
		}
		s.store.SaveContainer(c)
		r.Load()
		s.store.SaveReservation(r)
		loaded = append(loaded, r.ID)
	}
	if len(loaded) > 0 {
		slot.Load()
		s.store.SaveSlot(slot)
	}
	return loaded, nil
}

// ConfirmDelivery confirms delivery at the destination-port terminal.
func (s *DeliveryService) ConfirmDelivery(reservationID string, now time.Time) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	r, ok := s.store.GetReservation(reservationID)
	if !ok {
		return domain.ErrReservationNotFound
	}
	if r.Status != domain.ReservationLoaded {
		return domain.ErrInvalidState
	}
	c, ok := s.store.GetContainer(r.ContainerID)
	if !ok {
		return domain.ErrContainerNotFound
	}
	if err := c.Deliver(now); err != nil {
		return err
	}
	s.store.SaveContainer(c)
	r.Deliver()
	s.store.SaveReservation(r)
	s.store.AppendEvent(domain.DeliveryConfirmedEvent{
		ReservationID: reservationID,
		ContainerID:   r.ContainerID,
		OccurredAt:    now,
	})
	return nil
}

// CheckAutoRelease enforces the rule: cargo that has not arrived at the yard
// 24 hours before the window cutoff is auto-released, and the freed capacity
// is offered to the next waitlisted reservation. Returns released reservation IDs.
//
// The method uses a two-pass approach: first it identifies all reservations
// whose cargo has not arrived, then it releases them and promotes waitlisted
// reservations. This ensures that a reservation promoted from the waitlist in
// the current cycle is not immediately re-released.
func (s *DeliveryService) CheckAutoRelease(now time.Time) []string {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	// First pass: identify reservations to release before any state changes.
	var toRelease []string
	for _, w := range s.store.ListWindows() {
		cutoffMinus24h := w.CutoffTime.Add(-domain.AutoReleaseWindow)
		if now.Before(cutoffMinus24h) {
			continue
		}
		for _, r := range s.store.ReservationsByWindow(w.ID) {
			if r.Status != domain.ReservationLocked {
				continue
			}
			c, ok := s.store.GetContainer(r.ContainerID)
			if !ok || c.Status == domain.ContainerRegistered {
				toRelease = append(toRelease, r.ID)
			}
		}
	}

	// Second pass: release identified reservations and promote waitlist.
	var released []string
	for _, rid := range toRelease {
		r, ok := s.store.GetReservation(rid)
		if !ok || r.Status != domain.ReservationLocked {
			continue
		}
		slot, ok := s.store.GetSlot(r.SlotID)
		if !ok {
			continue
		}
		slot.Release()
		s.store.SaveSlot(slot)
		r.Release()
		s.store.SaveReservation(r)
		s.store.AppendEvent(domain.SlotReleasedEvent{
			ReservationID: r.ID,
			SlotID:        slot.ID,
			Reason:        "auto-release: cargo not arrived 24h before cutoff",
			OccurredAt:    now,
		})
		if s.notifier != nil {
			s.notifier.Notify(r.OwnerID,
				"slot auto-released: cargo not arrived 24h before cutoff for reservation "+r.ID)
		}
		released = append(released, r.ID)
		s.promoteWaitlistLocked(r.WindowID, slot, now)
	}
	return released
}

// promoteWaitlistLocked tries to fulfill waitlisted reservations after a slot
// gains capacity. Caller must hold the store mutex.
func (s *DeliveryService) promoteWaitlistLocked(windowID string, slot *domain.Slot, now time.Time) {
	window, ok := s.store.GetWindow(windowID)
	if !ok {
		return
	}
	for _, rid := range s.store.GetWaitlist(windowID) {
		if !slot.CanBook(*window) {
			break
		}
		r, ok := s.store.GetReservation(rid)
		if !ok || r.Status != domain.ReservationQueued {
			s.store.RemoveFromWaitlist(windowID, rid)
			continue
		}
		slot.Book()
		r.Lock(now)
		s.store.SaveSlot(slot)
		s.store.SaveReservation(r)
		s.store.RemoveFromWaitlist(windowID, rid)
		s.store.AppendEvent(domain.SlotLockedEvent{
			ReservationID: r.ID,
			SlotID:        slot.ID,
			WindowID:      windowID,
			OwnerID:       r.OwnerID,
			CargoType:     r.CargoType,
			OccurredAt:    now,
		})
		if s.notifier != nil {
			s.notifier.Notify(r.OwnerID,
				"waitlist promoted: slot locked for reservation "+r.ID)
		}
	}
}

// CheckTemperatureCompliance scans all in-transit temperature-controlled
// containers and returns alert messages for overdue or out-of-range readings.
func (s *DeliveryService) CheckTemperatureCompliance(now time.Time) []string {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	var alerts []string
	for _, c := range s.store.ListContainers() {
		if !c.CargoType.IsTemperatureControlled() {
			continue
		}
		if c.Status != domain.ContainerArrived && c.Status != domain.ContainerLoaded {
			continue
		}
		if c.IsReportingOverdue(now) {
			alerts = append(alerts, c.ID+": temperature reporting overdue")
			s.store.AppendEvent(domain.TemperatureAlertEvent{
				ContainerID: c.ID,
				Temperature: 0,
				OccurredAt:  now,
			})
		}
		if !c.IsTemperatureCompliant() {
			alerts = append(alerts, c.ID+": temperature out of range")
		}
	}
	return alerts
}

// PromoteWaitlist attempts to fulfill waitlisted reservations for a window
// when capacity becomes available (e.g. after oversell confirmation or a
// manual release). It is safe to call at any time.
func (s *DeliveryService) PromoteWaitlist(windowID string, now time.Time) {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()
	slot, ok := s.store.GetSlotByWindow(windowID)
	if !ok {
		return
	}
	s.promoteWaitlistLocked(windowID, slot, now)
}
