package store

import (
	"testing"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
)

func TestStore_SaveAndGetSlot(t *testing.T) {
	s := New()
	slot := &domain.Slot{
		ID:       "slot-1",
		WindowID: "win-1",
		VoyageID: "voy-1",
		Capacity: 10,
		Booked:   3,
		Status:   domain.SlotStatusLocked,
		Version:  1,
	}
	s.SaveSlot(slot)

	got, ok := s.GetSlot("slot-1")
	if !ok {
		t.Fatal("expected slot to exist")
	}
	if got.Booked != 3 {
		t.Fatalf("expected booked=3, got %d", got.Booked)
	}
	if got.Status != domain.SlotStatusLocked {
		t.Fatalf("expected status=locked, got %s", got.Status)
	}

	if _, ok := s.GetSlot("nonexistent"); ok {
		t.Fatal("expected nonexistent slot to return false")
	}
}

func TestStore_ReservationsByOwner(t *testing.T) {
	s := New()
	now := time.Now().UTC()

	r1 := &domain.Reservation{ID: "r1", OwnerID: "owner-A", SlotID: "s1", CargoType: domain.CargoTypeHighValue}
	r2 := &domain.Reservation{ID: "r2", OwnerID: "owner-B", SlotID: "s1", CargoType: domain.CargoTypeNormal}
	r3 := &domain.Reservation{ID: "r3", OwnerID: "owner-A", SlotID: "s2", CargoType: domain.CargoTypeNormal}

	s.SaveReservation(r1)
	s.SaveReservation(r2)
	s.SaveReservation(r3)

	got := s.ReservationsByOwner("owner-A")
	if len(got) != 2 {
		t.Fatalf("expected 2 reservations for owner-A, got %d", len(got))
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	if !ids["r1"] || !ids["r3"] {
		t.Fatalf("expected r1 and r3, got %v", ids)
	}
	_ = now
}

func TestStore_WaitlistOperations(t *testing.T) {
	s := New()
	windowID := "win-1"

	pos1 := s.AddToWaitlist(windowID, "res-1")
	if pos1 != 1 {
		t.Fatalf("expected position 1, got %d", pos1)
	}
	pos2 := s.AddToWaitlist(windowID, "res-2")
	if pos2 != 2 {
		t.Fatalf("expected position 2, got %d", pos2)
	}
	pos3 := s.AddToWaitlist(windowID, "res-3")
	if pos3 != 3 {
		t.Fatalf("expected position 3, got %d", pos3)
	}

	got := s.GetWaitlist(windowID)
	if len(got) != 3 {
		t.Fatalf("expected 3 waitlist entries, got %d", len(got))
	}

	s.RemoveFromWaitlist(windowID, "res-2")
	got = s.GetWaitlist(windowID)
	if len(got) != 2 {
		t.Fatalf("expected 2 after removal, got %d", len(got))
	}
	if got[0] != "res-1" || got[1] != "res-3" {
		t.Fatalf("unexpected waitlist order after removal: %v", got)
	}

	pos := s.WaitlistPosition(windowID, "res-3")
	if pos != 2 {
		t.Fatalf("expected res-3 at position 2, got %d", pos)
	}
	pos = s.WaitlistPosition(windowID, "res-2")
	if pos != 0 {
		t.Fatalf("expected position 0 for removed entry, got %d", pos)
	}
}

func TestStore_Snapshot(t *testing.T) {
	s := New()
	s.SaveWindow(&domain.PeakWindow{ID: "w1", Capacity: 10})
	s.SaveSlot(&domain.Slot{ID: "s1", WindowID: "w1", Capacity: 10})
	s.SaveContainer(&domain.Container{ID: "c1", OwnerID: "o1"})
	s.SaveReservation(&domain.Reservation{ID: "r1", SlotID: "s1"})
	s.AddToWaitlist("w1", "r2")

	snap := s.Snapshot()
	if len(snap.Windows) != 1 || len(snap.Slots) != 1 || len(snap.Containers) != 1 || len(snap.Reservations) != 1 {
		t.Fatalf("snapshot has unexpected counts: %+v", snap)
	}
	if len(snap.Waitlists["w1"]) != 1 || snap.Waitlists["w1"][0] != "r2" {
		t.Fatalf("unexpected waitlist in snapshot: %v", snap.Waitlists["w1"])
	}
	if snap.SavedAt.IsZero() {
		t.Fatal("expected non-zero SavedAt")
	}
}
