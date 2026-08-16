package service

import (
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/store"
)

// RecoveryResult summarises a batch reschedule operation.
type RecoveryResult struct {
	Rescheduled []string `json:"rescheduled"`
	Failed      []string `json:"failed"`
}

// RecoveryService handles failure recovery: slot freezing, waitlist rescheduling
// to the next voyage, single-reservation rescheduling (same route or Suez backup),
// and rollback-to-manual when rescheduling fails.
type RecoveryService struct {
	store    *store.Store
	notifier Notifier
}

// NewRecoveryService creates a RecoveryService.
func NewRecoveryService(s *store.Store, n Notifier) *RecoveryService {
	return &RecoveryService{store: s, notifier: n}
}

// FreezeSlot freezes a slot due to schedule delay or port rolling, notifies
// all affected cargo owners, and returns the affected reservation IDs.
func (s *RecoveryService) FreezeSlot(slotID, reason string, now time.Time) ([]string, error) {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	slot, ok := s.store.GetSlot(slotID)
	if !ok {
		return nil, domain.ErrSlotNotFound
	}
	slot.Freeze(reason)
	s.store.SaveSlot(slot)
	s.store.AppendEvent(domain.SlotFrozenEvent{
		SlotID:     slotID,
		Reason:     reason,
		OccurredAt: now,
	})

	var affected []string
	for _, r := range s.store.ReservationsBySlot(slotID) {
		if r.Status == domain.ReservationLocked || r.Status == domain.ReservationLoaded {
			affected = append(affected, r.ID)
			if s.notifier != nil {
				s.notifier.Notify(r.OwnerID, "slot frozen: "+reason+" (reservation "+r.ID+")")
			}
		}
	}
	return affected, nil
}

// UnfreezeSlot removes the frozen state from a slot.
func (s *RecoveryService) UnfreezeSlot(slotID string) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	slot, ok := s.store.GetSlot(slotID)
	if !ok {
		return domain.ErrSlotNotFound
	}
	slot.Unfreeze()
	s.store.SaveSlot(slot)
	return nil
}

// RescheduleFromWaitlist reschedules waitlisted cargo for the same destination
// port to the next voyage's slot. Reservations that cannot be accommodated
// are flagged for manual handling (rollback).
func (s *RecoveryService) RescheduleFromWaitlist(fromWindowID string, toSlotID string, now time.Time) (*RecoveryResult, error) {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	fromWindow, ok := s.store.GetWindow(fromWindowID)
	if !ok {
		return nil, domain.ErrWindowNotFound
	}
	toSlot, ok := s.store.GetSlot(toSlotID)
	if !ok {
		return nil, domain.ErrSlotNotFound
	}
	toWindow, ok := s.store.GetWindow(toSlot.WindowID)
	if !ok {
		return nil, domain.ErrWindowNotFound
	}

	result := &RecoveryResult{}
	for _, rid := range s.store.GetWaitlist(fromWindowID) {
		r, ok := s.store.GetReservation(rid)
		if !ok || r.Status != domain.ReservationQueued {
			s.store.RemoveFromWaitlist(fromWindowID, rid)
			continue
		}
		c, ok := s.store.GetContainer(r.ContainerID)
		if !ok {
			continue
		}
		// Only reschedule cargo with the same destination port.
		if c.DestinationPort != fromWindow.DestinationPort {
			continue
		}

		if !toSlot.CanBook(*toWindow) {
			result.Failed = append(result.Failed, rid)
			s.flagManualLocked(r, "reschedule failed: next slot at capacity", now)
			continue
		}

		toSlot.Book()
		oldSlotID := r.SlotID
		r.SlotID = toSlot.ID
		r.WindowID = toSlot.WindowID
		r.Reschedule(now)
		s.store.SaveSlot(toSlot)
		s.store.SaveReservation(r)
		s.store.RemoveFromWaitlist(fromWindowID, rid)
		s.store.AppendEvent(domain.RescheduleEvent{
			ReservationID: r.ID,
			FromSlotID:    oldSlotID,
			ToSlotID:      toSlot.ID,
			OccurredAt:    now,
		})
		result.Rescheduled = append(result.Rescheduled, rid)
		if s.notifier != nil {
			s.notifier.Notify(r.OwnerID, "reservation rescheduled to next voyage: "+r.ID)
		}
	}
	return result, nil
}

// RescheduleReservation moves a single reservation to the next voyage on the
// same route. Switching to the Suez backup route requires written cargo-owner
// consent. If the target slot is full, the old slot is left intact and the
// reservation is flagged for manual handling (rollback).
func (s *RecoveryService) RescheduleReservation(reservationID, nextSlotID string, useSuez bool, suezConsent bool, now time.Time) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	r, ok := s.store.GetReservation(reservationID)
	if !ok {
		return domain.ErrReservationNotFound
	}

	// Rule: Suez backup requires written cargo-owner consent.
	if useSuez && !suezConsent {
		return domain.ErrSuezConsentRequired
	}

	oldSlot, ok := s.store.GetSlot(r.SlotID)
	if !ok {
		return domain.ErrSlotNotFound
	}
	nextSlot, ok := s.store.GetSlot(nextSlotID)
	if !ok {
		return domain.ErrSlotNotFound
	}
	oldWindow, ok := s.store.GetWindow(oldSlot.WindowID)
	if !ok {
		return domain.ErrWindowNotFound
	}
	nextWindow, ok := s.store.GetWindow(nextSlot.WindowID)
	if !ok {
		return domain.ErrWindowNotFound
	}

	// Rule: reschedule only to next voyage on the same route (unless Suez backup).
	if !useSuez {
		if !oldWindow.IsSameRoute(*nextWindow) {
			return domain.ErrSameRouteOnly
		}
		if nextSlot.VoyageID == oldSlot.VoyageID {
			return domain.ErrSameVoyage
		}
	}

	// Check capacity BEFORE releasing the old slot to avoid orphaned capacity.
	if !nextSlot.CanBook(*nextWindow) {
		s.flagManualLocked(r, "reschedule failed: next slot at capacity", now)
		return domain.ErrSlotFull
	}

	// Release old slot, book new slot, update reservation.
	oldSlot.Release()
	s.store.SaveSlot(oldSlot)

	nextSlot.Book()
	if useSuez {
		nextSlot.SuezBackup = true
	}
	s.store.SaveSlot(nextSlot)

	r.SlotID = nextSlot.ID
	r.WindowID = nextSlot.WindowID
	r.SuezConsent = suezConsent
	r.Reschedule(now)
	s.store.SaveReservation(r)

	s.store.AppendEvent(domain.RescheduleEvent{
		ReservationID: reservationID,
		FromSlotID:    oldSlot.ID,
		ToSlotID:      nextSlot.ID,
		SuezBackup:    useSuez,
		OccurredAt:    now,
	})

	if s.notifier != nil {
		msg := "reservation rescheduled to next voyage: " + reservationID
		if useSuez {
			msg = "reservation rescheduled to Suez backup route: " + reservationID
		}
		s.notifier.Notify(r.OwnerID, msg)
	}
	return nil
}

// flagManualLocked marks a reservation for manual handling and emits an event.
// Caller must hold the store mutex.
func (s *RecoveryService) flagManualLocked(r *domain.Reservation, reason string, now time.Time) {
	r.ManualFlag = true
	s.store.SaveReservation(r)
	s.store.AppendEvent(domain.ManualInterventionEvent{
		ReservationID: r.ID,
		Reason:        reason,
		OccurredAt:    now,
	})
	if s.notifier != nil {
		s.notifier.Notify(r.OwnerID, "manual intervention required: "+reason+" (reservation "+r.ID+")")
	}
}
