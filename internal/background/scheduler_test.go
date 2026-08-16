package background

import (
	"context"
	"testing"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/service"
	"github.com/arctic-express/scheduler/internal/store"
)

type silentNotifier struct{}

func (n *silentNotifier) Notify(string, string) {}

func TestScheduler_AutoRelease(t *testing.T) {
	s := store.New()
	cutoff := time.Now().Add(12 * time.Hour) // 12h from now → past the 24h-before mark
	s.SaveWindow(&domain.PeakWindow{
		ID: "win-1", VoyageID: "voy-1", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff, Capacity: 1, OversellRatio: 0.1,
	})
	s.SaveSlot(&domain.Slot{ID: "slot-1", WindowID: "win-1", Capacity: 1, Status: domain.SlotStatusAvailable})
	s.SaveContainer(&domain.Container{
		ID: "c1", OwnerID: "o1", CargoType: domain.CargoTypeNormal, DestinationPort: "NLRTM",
		Status: domain.ContainerRegistered,
	})
	s.SaveReservation(&domain.Reservation{
		ID: "req-1", SlotID: "slot-1", WindowID: "win-1", ContainerID: "c1",
		OwnerID: "o1", CargoType: domain.CargoTypeNormal, Status: domain.ReservationLocked,
	})
	slot, _ := s.GetSlot("slot-1")
	slot.Booked = 1
	slot.Status = domain.SlotStatusLocked

	notify := &silentNotifier{}
	delSvc := service.NewDeliveryService(s, notify)
	sched := New(delSvc, 50*time.Millisecond)
	sched.Start()
	defer sched.Stop()

	// Wait for at least one tick.
	time.Sleep(200 * time.Millisecond)

	r, _ := s.GetReservation("req-1")
	if r.Status != domain.ReservationReleased {
		t.Fatalf("expected auto-released by scheduler, got %s", r.Status)
	}
}

func TestScheduler_TemperatureAlert(t *testing.T) {
	s := store.New()
	s.SaveWindow(&domain.PeakWindow{ID: "win-1", CutoffTime: time.Now().Add(48 * time.Hour), Capacity: 5, OversellRatio: 0.1})
	s.SaveSlot(&domain.Slot{ID: "slot-1", WindowID: "win-1", Capacity: 5, Status: domain.SlotStatusAvailable})
	s.SaveContainer(&domain.Container{
		ID: "c-bat", OwnerID: "o1", CargoType: domain.CargoTypePowerBattery, DestinationPort: "NLRTM",
		Status: domain.ContainerArrived,
	})
	// The container has no readings → reporting overdue.

	notify := &silentNotifier{}
	delSvc := service.NewDeliveryService(s, notify)
	sched := New(delSvc, 50*time.Millisecond)
	sched.Start()
	defer sched.Stop()

	// Wait for at least one tick.
	time.Sleep(200 * time.Millisecond)

	events := s.Events()
	foundAlert := false
	for _, e := range events {
		if e.EventName() == "temperature.alert" {
			foundAlert = true
		}
	}
	if !foundAlert {
		t.Fatal("expected a temperature alert event from the scheduler")
	}
}

func TestScheduler_RunOnce(t *testing.T) {
	s := store.New()
	s.SaveWindow(&domain.PeakWindow{ID: "win-1", CutoffTime: time.Now().Add(48 * time.Hour), Capacity: 5, OversellRatio: 0.1})
	s.SaveSlot(&domain.Slot{ID: "slot-1", WindowID: "win-1", Capacity: 5, Status: domain.SlotStatusAvailable})

	notify := &silentNotifier{}
	delSvc := service.NewDeliveryService(s, notify)
	sched := New(delSvc, time.Hour)

	// Should not block or panic.
	sched.RunOnce(context.Background())
}
