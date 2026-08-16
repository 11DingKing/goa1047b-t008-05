package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/store"
)

// mockNotifier records all notifications for assertion in tests.
type mockNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (n *mockNotifier) Notify(ownerID string, message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, ownerID+": "+message)
}

func (n *mockNotifier) Messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.messages))
	copy(out, n.messages)
	return out
}

// setupWindow creates a store with one window, one slot, and returns them.
func setupWindow(t *testing.T, capacity int, oversellRatio float64, cutoff time.Time) (*store.Store, *domain.PeakWindow, *domain.Slot) {
	t.Helper()
	s := store.New()
	w := &domain.PeakWindow{
		ID:              "win-1",
		VoyageID:        "voy-1",
		OriginPort:      "CNYTN",
		DestinationPort: "NLRTM",
		CutoffTime:      cutoff,
		StartTime:       cutoff.Add(-48 * time.Hour),
		EndTime:         cutoff.Add(24 * time.Hour),
		Capacity:        capacity,
		OversellRatio:   oversellRatio,
	}
	slot := &domain.Slot{
		ID:       "slot-1",
		WindowID: "win-1",
		VoyageID: "voy-1",
		Capacity: capacity,
		Status:   domain.SlotStatusAvailable,
	}
	s.SaveWindow(w)
	s.SaveSlot(slot)
	return s, w, slot
}

func registerContainer(s *store.Store, id, owner string, cargo domain.CargoType, destPort string) {
	s.SaveContainer(&domain.Container{
		ID:              id,
		OwnerID:         owner,
		CargoType:       cargo,
		DestinationPort: destPort,
		Status:          domain.ContainerRegistered,
	})
}

// --- Reservation tests ---

func TestReservationService_LockSlotSuccess(t *testing.T) {
	s, _, _ := setupWindow(t, 5, 0.1, time.Now().Add(48*time.Hour))
	registerContainer(s, "c1", "owner-1", domain.CargoTypeNormal, "NLRTM")
	notify := &mockNotifier{}
	resSvc := NewReservationService(s, notify, 10*time.Millisecond)

	result, err := resSvc.LockSlot(context.Background(), LockSlotRequest{
		RequestID:   "req-1",
		WindowID:    "win-1",
		ContainerID: "c1",
		OwnerID:     "owner-1",
	}, domain.CargoTypeNormal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected lock success, got: %+v", result)
	}
	if result.ReservationID != "req-1" {
		t.Fatalf("expected reservation ID req-1, got %s", result.ReservationID)
	}

	r, ok := s.GetReservation("req-1")
	if !ok {
		t.Fatal("expected reservation to be persisted")
	}
	if r.Status != domain.ReservationLocked {
		t.Fatalf("expected status locked, got %s", r.Status)
	}
	slot, _ := s.GetSlot("slot-1")
	if slot.Booked != 1 {
		t.Fatalf("expected booked=1, got %d", slot.Booked)
	}
}

func TestReservationService_ConcurrentPrioritySorting(t *testing.T) {
	s, _, _ := setupWindow(t, 1, 0.1, time.Now().Add(48*time.Hour))
	registerContainer(s, "c-normal", "owner-normal", domain.CargoTypeNormal, "NLRTM")
	registerContainer(s, "c-high", "owner-high", domain.CargoTypeHighValue, "NLRTM")
	registerContainer(s, "c-energy", "owner-energy", domain.CargoTypeEnergyStorage, "NLRTM")

	notify := &mockNotifier{}
	// Use a longer batch delay so all three concurrent requests land in the same batch.
	resSvc := NewReservationService(s, notify, 50*time.Millisecond)
	defer resSvc.Close()

	reqs := []struct {
		reqID string
		cid   string
		owner string
		cargo domain.CargoType
	}{
		{"req-normal", "c-normal", "owner-normal", domain.CargoTypeNormal},
		{"req-high", "c-high", "owner-high", domain.CargoTypeHighValue},
		{"req-energy", "c-energy", "owner-energy", domain.CargoTypeEnergyStorage},
	}

	results := make([]*LockResult, len(reqs))
	var wg sync.WaitGroup
	wg.Add(len(reqs))
	barrier := make(chan struct{})

	for i, r := range reqs {
		go func(idx int, req LockSlotRequest, cargo domain.CargoType) {
			defer wg.Done()
			<-barrier
			result, _ := resSvc.LockSlot(context.Background(), req, cargo)
			results[idx] = result
		}(i, LockSlotRequest{
			RequestID:   r.reqID,
			WindowID:    "win-1",
			ContainerID: r.cid,
			OwnerID:     r.owner,
		}, r.cargo)
	}
	close(barrier)
	wg.Wait()

	// The high-value request should have succeeded.
	highIdx := -1
	for i, r := range reqs {
		if r.reqID == "req-high" {
			highIdx = i
		}
	}
	if highIdx < 0 || !results[highIdx].Success {
		t.Fatalf("expected high-value request to succeed, got: %+v", results[highIdx])
	}

	// The other two should be in the waitlist.
	queuedCount := 0
	for i, r := range results {
		if i == highIdx {
			continue
		}
		if r.QueuePosition > 0 {
			queuedCount++
		}
	}
	if queuedCount != 2 {
		t.Fatalf("expected 2 queued, got %d", queuedCount)
	}

	// Verify waitlist ordering: high-value is NOT in waitlist.
	wl := s.GetWaitlist("win-1")
	if len(wl) != 2 {
		t.Fatalf("expected 2 in waitlist, got %d", len(wl))
	}
	for _, id := range wl {
		if id == "req-high" {
			t.Fatal("high-value request should not be in waitlist")
		}
	}
}

func TestReservationService_OversellRequiresConfirmation(t *testing.T) {
	// Capacity 2, oversell ratio 0.5 → effective capacity 3 when confirmed.
	s, _, _ := setupWindow(t, 2, 0.5, time.Now().Add(48*time.Hour))
	registerContainer(s, "c1", "o1", domain.CargoTypeNormal, "NLRTM")
	registerContainer(s, "c2", "o2", domain.CargoTypeNormal, "NLRTM")
	registerContainer(s, "c3", "o3", domain.CargoTypeNormal, "NLRTM")

	notify := &mockNotifier{}
	resSvc := NewReservationService(s, notify, 5*time.Millisecond)
	defer resSvc.Close()
	delSvc := NewDeliveryService(s, notify)

	// Fill base capacity.
	r1, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-1", "win-1", "c1", "o1"}, domain.CargoTypeNormal)
	r2, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-2", "win-1", "c2", "o2"}, domain.CargoTypeNormal)
	if !r1.Success || !r2.Success {
		t.Fatalf("expected first two locks to succeed: %+v %+v", r1, r2)
	}

	// Third request needs oversell → should go to waitlist.
	r3, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-3", "win-1", "c3", "o3"}, domain.CargoTypeNormal)
	if r3.Success {
		t.Fatal("expected third lock to be queued (oversell not confirmed)")
	}
	if r3.QueuePosition != 1 {
		t.Fatalf("expected queue position 1, got %d", r3.QueuePosition)
	}

	// Confirm oversell and promote waitlist.
	if err := resSvc.ConfirmOversell("slot-1"); err != nil {
		t.Fatalf("confirm oversell: %v", err)
	}
	delSvc.PromoteWaitlist("win-1", time.Now().UTC())

	// Third reservation should now be locked.
	r, ok := s.GetReservation("req-3")
	if !ok {
		t.Fatal("expected reservation req-3 to exist")
	}
	if r.Status != domain.ReservationLocked {
		t.Fatalf("expected req-3 to be promoted to locked, got %s", r.Status)
	}
	slot, _ := s.GetSlot("slot-1")
	if slot.Booked != 3 {
		t.Fatalf("expected booked=3 after oversell, got %d", slot.Booked)
	}
}

func TestReservationService_Idempotency(t *testing.T) {
	s, _, _ := setupWindow(t, 5, 0.1, time.Now().Add(48*time.Hour))
	registerContainer(s, "c1", "o1", domain.CargoTypeNormal, "NLRTM")
	notify := &mockNotifier{}
	resSvc := NewReservationService(s, notify, 5*time.Millisecond)
	defer resSvc.Close()

	req := LockSlotRequest{"req-idem", "win-1", "c1", "o1"}
	r1, err := resSvc.LockSlot(context.Background(), req, domain.CargoTypeNormal)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	r2, err := resSvc.LockSlot(context.Background(), req, domain.CargoTypeNormal)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("expected same result for idempotent call: %+v vs %+v", r1, r2)
	}
	if !r1.Success {
		t.Fatal("expected success")
	}

	// Only one reservation should exist.
	slot, _ := s.GetSlot("slot-1")
	if slot.Booked != 1 {
		t.Fatalf("expected booked=1 (idempotent), got %d", slot.Booked)
	}
}

// --- Delivery tests ---

func TestDeliveryService_AutoRelease(t *testing.T) {
	// Cutoff is 12 hours from now → 24h-before-cutoff has already passed.
	cutoff := time.Now().Add(12 * time.Hour)
	s, _, _ := setupWindow(t, 1, 0.1, cutoff)
	registerContainer(s, "c1", "o1", domain.CargoTypeNormal, "NLRTM")
	registerContainer(s, "c2", "o2", domain.CargoTypeNormal, "NLRTM")

	notify := &mockNotifier{}
	resSvc := NewReservationService(s, notify, 5*time.Millisecond)
	defer resSvc.Close()
	delSvc := NewDeliveryService(s, notify)

	// Lock the only slot.
	r1, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-1", "win-1", "c1", "o1"}, domain.CargoTypeNormal)
	if !r1.Success {
		t.Fatal("expected first lock to succeed")
	}
	// Second request goes to waitlist.
	r2, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-2", "win-1", "c2", "o2"}, domain.CargoTypeNormal)
	if r2.Success {
		t.Fatal("expected second lock to be queued")
	}

	// Container c1 never arrives → auto-release should free the slot.
	released := delSvc.CheckAutoRelease(time.Now().UTC())
	if len(released) != 1 || released[0] != "req-1" {
		t.Fatalf("expected req-1 to be auto-released, got %v", released)
	}

	r, _ := s.GetReservation("req-1")
	if r.Status != domain.ReservationReleased {
		t.Fatalf("expected released status, got %s", r.Status)
	}

	// The waitlisted req-2 should be promoted.
	r2res, _ := s.GetReservation("req-2")
	if r2res.Status != domain.ReservationLocked {
		t.Fatalf("expected req-2 promoted to locked, got %s", r2res.Status)
	}
}

func TestDeliveryService_TemperatureCompliance(t *testing.T) {
	s, _, _ := setupWindow(t, 5, 0.1, time.Now().Add(48*time.Hour))
	notify := &mockNotifier{}
	delSvc := NewDeliveryService(s, notify)

	registerContainer(s, "c-battery", "o1", domain.CargoTypePowerBattery, "NLRTM")
	// Arrive at yard.
	if err := delSvc.RecordCargoArrival("c-battery", time.Now().UTC()); err != nil {
		t.Fatalf("arrival: %v", err)
	}

	// Good reading (within 15–25 °C).
	if err := delSvc.RecordTemperature("c-battery", time.Now().UTC(), 20.0); err != nil {
		t.Fatalf("good temp: %v", err)
	}

	// Bad reading (above 25 °C).
	if err := delSvc.RecordTemperature("c-battery", time.Now().UTC(), 30.0); err != nil {
		t.Fatalf("bad temp: %v", err)
	}

	c, _ := s.GetContainer("c-battery")
	if c.IsTemperatureCompliant() {
		t.Fatal("expected non-compliant after 30C reading")
	}

	alerts := delSvc.CheckTemperatureCompliance(time.Now().UTC())
	if len(alerts) == 0 {
		t.Fatal("expected temperature alert")
	}

	// Non-temperature-controlled container should error.
	registerContainer(s, "c-normal", "o2", domain.CargoTypeNormal, "NLRTM")
	if err := delSvc.RecordTemperature("c-normal", time.Now().UTC(), 20.0); err != domain.ErrNotTemperatureControlled {
		t.Fatalf("expected ErrNotTemperatureControlled, got %v", err)
	}
}

func TestDeliveryService_LoadingManifest(t *testing.T) {
	s, _, _ := setupWindow(t, 5, 0.1, time.Now().Add(48*time.Hour))
	notify := &mockNotifier{}
	resSvc := NewReservationService(s, notify, 5*time.Millisecond)
	defer resSvc.Close()
	delSvc := NewDeliveryService(s, notify)

	registerContainer(s, "c1", "o1", domain.CargoTypeNormal, "NLRTM")
	registerContainer(s, "c2", "o2", domain.CargoTypeNormal, "NLRTM")

	r1, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-1", "win-1", "c1", "o1"}, domain.CargoTypeNormal)
	r2, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-2", "win-1", "c2", "o2"}, domain.CargoTypeNormal)
	if !r1.Success || !r2.Success {
		t.Fatal("expected both locks to succeed")
	}

	// c1 arrives, c2 does not.
	delSvc.RecordCargoArrival("c1", time.Now().UTC())

	now := time.Now().UTC()
	loaded, err := delSvc.VerifyLoadingManifest("slot-1", now)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(loaded) != 1 || loaded[0] != "req-1" {
		t.Fatalf("expected only req-1 loaded, got %v", loaded)
	}

	c1, _ := s.GetContainer("c1")
	if c1.Status != domain.ContainerLoaded {
		t.Fatalf("expected c1 loaded, got %s", c1.Status)
	}

	// Delivery confirmation.
	if err := delSvc.ConfirmDelivery("req-1", now); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	r, _ := s.GetReservation("req-1")
	if r.Status != domain.ReservationDelivered {
		t.Fatalf("expected delivered, got %s", r.Status)
	}
}

// --- Recovery tests ---

func TestRecoveryService_FreezeAndRescheduleFromWaitlist(t *testing.T) {
	// Set up two windows/voyages on the same route.
	s := store.New()
	cutoff := time.Now().Add(48 * time.Hour)
	w1 := &domain.PeakWindow{
		ID: "win-1", VoyageID: "voy-1", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff, StartTime: cutoff.Add(-48 * time.Hour), EndTime: cutoff.Add(24 * time.Hour),
		Capacity: 1, OversellRatio: 0.1,
	}
	w2 := &domain.PeakWindow{
		ID: "win-2", VoyageID: "voy-2", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff.Add(72 * time.Hour), StartTime: cutoff.Add(24 * time.Hour), EndTime: cutoff.Add(96 * time.Hour),
		Capacity: 2, OversellRatio: 0.1,
	}
	slot1 := &domain.Slot{ID: "slot-1", WindowID: "win-1", VoyageID: "voy-1", Capacity: 1, Status: domain.SlotStatusAvailable}
	slot2 := &domain.Slot{ID: "slot-2", WindowID: "win-2", VoyageID: "voy-2", Capacity: 2, Status: domain.SlotStatusAvailable}
	s.SaveWindow(w1)
	s.SaveWindow(w2)
	s.SaveSlot(slot1)
	s.SaveSlot(slot2)

	registerContainer(s, "c1", "o1", domain.CargoTypeNormal, "NLRTM")
	registerContainer(s, "c2", "o2", domain.CargoTypeNormal, "NLRTM")

	notify := &mockNotifier{}
	resSvc := NewReservationService(s, notify, 5*time.Millisecond)
	defer resSvc.Close()
	recSvc := NewRecoveryService(s, notify)

	// Lock the only slot-1 capacity.
	r1, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-1", "win-1", "c1", "o1"}, domain.CargoTypeNormal)
	if !r1.Success {
		t.Fatal("expected first lock to succeed")
	}
	// Second request goes to waitlist.
	r2, _ := resSvc.LockSlot(context.Background(), LockSlotRequest{"req-2", "win-1", "c2", "o2"}, domain.CargoTypeNormal)
	if r2.Success {
		t.Fatal("expected second to be queued")
	}

	// Freeze slot-1 (schedule delay).
	now := time.Now().UTC()
	affected, err := recSvc.FreezeSlot("slot-1", "schedule delay", now)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if len(affected) != 1 {
		t.Fatalf("expected 1 affected reservation, got %d", len(affected))
	}

	// Reschedule waitlisted cargo to next voyage.
	result, err := recSvc.RescheduleFromWaitlist("win-1", "slot-2", now)
	if err != nil {
		t.Fatalf("reschedule waitlist: %v", err)
	}
	if len(result.Rescheduled) != 1 || result.Rescheduled[0] != "req-2" {
		t.Fatalf("expected req-2 rescheduled, got %+v", result)
	}

	r2res, _ := s.GetReservation("req-2")
	if r2res.Status != domain.ReservationRescheduled {
		t.Fatalf("expected rescheduled status, got %s", r2res.Status)
	}
}

func TestRecoveryService_RescheduleSameRouteOnly(t *testing.T) {
	s := store.New()
	cutoff := time.Now().Add(48 * time.Hour)
	w1 := &domain.PeakWindow{
		ID: "win-1", VoyageID: "voy-1", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff, StartTime: cutoff.Add(-48 * time.Hour), EndTime: cutoff.Add(24 * time.Hour),
		Capacity: 5, OversellRatio: 0.1,
	}
	// Different destination port.
	w2 := &domain.PeakWindow{
		ID: "win-2", VoyageID: "voy-2", OriginPort: "CNYTN", DestinationPort: "DEHAM",
		CutoffTime: cutoff.Add(72 * time.Hour), StartTime: cutoff.Add(24 * time.Hour), EndTime: cutoff.Add(96 * time.Hour),
		Capacity: 5, OversellRatio: 0.1,
	}
	slot1 := &domain.Slot{ID: "slot-1", WindowID: "win-1", VoyageID: "voy-1", Capacity: 5, Status: domain.SlotStatusAvailable}
	slot2 := &domain.Slot{ID: "slot-2", WindowID: "win-2", VoyageID: "voy-2", Capacity: 5, Status: domain.SlotStatusAvailable}
	s.SaveWindow(w1)
	s.SaveWindow(w2)
	s.SaveSlot(slot1)
	s.SaveSlot(slot2)

	registerContainer(s, "c1", "o1", domain.CargoTypeNormal, "NLRTM")
	s.SaveReservation(&domain.Reservation{
		ID: "req-1", SlotID: "slot-1", WindowID: "win-1", ContainerID: "c1",
		OwnerID: "o1", CargoType: domain.CargoTypeNormal, Status: domain.ReservationLocked,
	})
	slot1.Booked = 1
	slot1.Status = domain.SlotStatusLocked
	s.SaveSlot(slot1)

	notify := &mockNotifier{}
	recSvc := NewRecoveryService(s, notify)

	err := recSvc.RescheduleReservation("req-1", "slot-2", false, false, time.Now().UTC())
	if err != domain.ErrSameRouteOnly {
		t.Fatalf("expected ErrSameRouteOnly, got %v", err)
	}
}

func TestRecoveryService_SuezConsentRequired(t *testing.T) {
	s := store.New()
	cutoff := time.Now().Add(48 * time.Hour)
	w1 := &domain.PeakWindow{
		ID: "win-1", VoyageID: "voy-1", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff, Capacity: 5, OversellRatio: 0.1,
	}
	w2 := &domain.PeakWindow{
		ID: "win-2", VoyageID: "voy-2", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff.Add(72 * time.Hour), Capacity: 5, OversellRatio: 0.1,
	}
	slot1 := &domain.Slot{ID: "slot-1", WindowID: "win-1", VoyageID: "voy-1", Capacity: 5, Status: domain.SlotStatusLocked, Booked: 1}
	slot2 := &domain.Slot{ID: "slot-2", WindowID: "win-2", VoyageID: "voy-2", Capacity: 5, Status: domain.SlotStatusAvailable}
	s.SaveWindow(w1)
	s.SaveWindow(w2)
	s.SaveSlot(slot1)
	s.SaveSlot(slot2)
	s.SaveReservation(&domain.Reservation{
		ID: "req-1", SlotID: "slot-1", WindowID: "win-1", OwnerID: "o1",
		CargoType: domain.CargoTypeNormal, Status: domain.ReservationLocked,
	})

	notify := &mockNotifier{}
	recSvc := NewRecoveryService(s, notify)

	// Without consent → error.
	err := recSvc.RescheduleReservation("req-1", "slot-2", true, false, time.Now().UTC())
	if err != domain.ErrSuezConsentRequired {
		t.Fatalf("expected ErrSuezConsentRequired, got %v", err)
	}

	// With consent → success.
	err = recSvc.RescheduleReservation("req-1", "slot-2", true, true, time.Now().UTC())
	if err != nil {
		t.Fatalf("expected success with consent, got %v", err)
	}

	r, _ := s.GetReservation("req-1")
	if !r.SuezConsent {
		t.Fatal("expected suez consent to be recorded")
	}
}

func TestRecoveryService_RescheduleFailureRollback(t *testing.T) {
	s := store.New()
	cutoff := time.Now().Add(48 * time.Hour)
	w1 := &domain.PeakWindow{
		ID: "win-1", VoyageID: "voy-1", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff, Capacity: 1, OversellRatio: 0.1,
	}
	w2 := &domain.PeakWindow{
		ID: "win-2", VoyageID: "voy-2", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff.Add(72 * time.Hour), Capacity: 1, OversellRatio: 0.1,
	}
	// slot-1 has 1 booking (full).
	slot1 := &domain.Slot{ID: "slot-1", WindowID: "win-1", VoyageID: "voy-1", Capacity: 1, Booked: 1, Status: domain.SlotStatusLocked}
	// slot-2 is also full.
	slot2 := &domain.Slot{ID: "slot-2", WindowID: "win-2", VoyageID: "voy-2", Capacity: 1, Booked: 1, Status: domain.SlotStatusLocked}
	s.SaveWindow(w1)
	s.SaveWindow(w2)
	s.SaveSlot(slot1)
	s.SaveSlot(slot2)
	s.SaveReservation(&domain.Reservation{
		ID: "req-1", SlotID: "slot-1", WindowID: "win-1", OwnerID: "o1",
		CargoType: domain.CargoTypeNormal, Status: domain.ReservationLocked,
	})

	notify := &mockNotifier{}
	recSvc := NewRecoveryService(s, notify)

	// Try to reschedule to a full slot → should fail and flag manual.
	err := recSvc.RescheduleReservation("req-1", "slot-2", false, false, time.Now().UTC())
	if err != domain.ErrSlotFull {
		t.Fatalf("expected ErrSlotFull, got %v", err)
	}

	// Old slot should be intact (not released).
	slot1After, _ := s.GetSlot("slot-1")
	if slot1After.Booked != 1 {
		t.Fatalf("expected old slot booked=1 (rollback), got %d", slot1After.Booked)
	}

	// Reservation should be flagged for manual handling.
	r, _ := s.GetReservation("req-1")
	if !r.ManualFlag {
		t.Fatal("expected manual flag to be set after rollback")
	}
}
