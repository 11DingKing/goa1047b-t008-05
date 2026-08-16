package transport

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
)

// manifestEnv wires one window/slot pair plus helpers to put arrived cargo with
// a locked reservation on the manifest.
type manifestEnv struct {
	env    *testEnv
	router http.Handler
}

func newManifestEnv(t *testing.T) *manifestEnv {
	t.Helper()
	env := newTestEnv(t)
	router := NewRouter(env.handler)

	cutoff := time.Now().Add(48 * time.Hour)
	env.store.Mu.Lock()
	env.store.SaveWindow(&domain.PeakWindow{
		ID: "win-1", VoyageID: "voy-1", OriginPort: "CNYTN", DestinationPort: "NLRTM",
		CutoffTime: cutoff, StartTime: cutoff.Add(-48 * time.Hour), EndTime: cutoff.Add(24 * time.Hour),
		Capacity: 10, OversellRatio: 0.1,
	})
	env.store.SaveSlot(&domain.Slot{
		ID: "slot-1", WindowID: "win-1", VoyageID: "voy-1", Capacity: 10,
		Status: domain.SlotStatusLocked,
	})
	env.store.Mu.Unlock()
	return &manifestEnv{env: env, router: router}
}

// addArrivedCargo puts a container in the yard with a locked reservation.
func (m *manifestEnv) addArrivedCargo(t *testing.T, reservationID string, cargo domain.CargoType) {
	t.Helper()
	containerID := "c-" + reservationID
	arrivedAt := time.Now().UTC()
	m.env.store.Mu.Lock()
	defer m.env.store.Mu.Unlock()
	m.env.store.SaveContainer(&domain.Container{
		ID: containerID, OwnerID: "owner-" + reservationID, CargoType: cargo,
		DestinationPort: "NLRTM", Status: domain.ContainerArrived, ArrivedAt: &arrivedAt,
	})
	m.env.store.SaveReservation(&domain.Reservation{
		ID: reservationID, SlotID: "slot-1", WindowID: "win-1", ContainerID: containerID,
		OwnerID: "owner-" + reservationID, CargoType: cargo,
		Status: domain.ReservationLocked, CreatedAt: time.Now().UTC(),
	})
	slot, _ := m.env.store.GetSlot("slot-1")
	slot.Booked++
	m.env.store.SaveSlot(slot)
}

// reportTemperature pushes one reading through the public API.
func (m *manifestEnv) reportTemperature(t *testing.T, reservationID string, temp float64) {
	t.Helper()
	rec := doJSON(t, m.router, "POST", "/api/containers/c-"+reservationID+"/temperature", map[string]any{
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"temperature": temp,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("report %.1fC for %s: status = %d, want 200 (%s)", temp, reservationID, rec.Code, rec.Body.String())
	}
}

// verifyManifest runs the loading-manifest check and returns the loaded IDs.
func (m *manifestEnv) verifyManifest(t *testing.T) []string {
	t.Helper()
	rec := doJSON(t, m.router, "POST", "/api/slots/slot-1/manifest", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify manifest: status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var payload map[string][]string
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode manifest response: %v", err)
	}
	return payload["loaded"]
}

func (m *manifestEnv) containerStatus(t *testing.T, reservationID string) domain.ContainerStatus {
	t.Helper()
	m.env.store.Mu.Lock()
	defer m.env.store.Mu.Unlock()
	c, ok := m.env.store.GetContainer("c-" + reservationID)
	if !ok {
		t.Fatalf("container for %s disappeared", reservationID)
	}
	return c.Status
}

// TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading covers the loading
// manifest for temperature-controlled cargo whose monitoring gateway never
// reported anything: with no reading on file the cargo may not be declared
// temperature compliant, so it must be held back instead of loaded.
func TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading(t *testing.T) {
	m := newManifestEnv(t)
	defer m.env.resSvc.Close()

	m.addArrivedCargo(t, "req-battery", domain.CargoTypePowerBattery)
	m.addArrivedCargo(t, "req-cabinet", domain.CargoTypeEnergyStorage)
	m.addArrivedCargo(t, "req-normal", domain.CargoTypeNormal)

	loaded := m.verifyManifest(t)

	for _, id := range loaded {
		if id == "req-battery" || id == "req-cabinet" {
			t.Fatalf("%s was loaded without a single temperature reading; loaded = %v", id, loaded)
		}
	}
	if len(loaded) != 1 || loaded[0] != "req-normal" {
		t.Fatalf("loaded = %v, want only [req-normal]", loaded)
	}
	if got := m.containerStatus(t, "req-battery"); got != domain.ContainerArrived {
		t.Fatalf("power battery container status = %s, want it to stay %s", got, domain.ContainerArrived)
	}
	if got := m.containerStatus(t, "req-cabinet"); got != domain.ContainerArrived {
		t.Fatalf("energy storage container status = %s, want it to stay %s", got, domain.ContainerArrived)
	}
	if got := m.containerStatus(t, "req-normal"); got != domain.ContainerLoaded {
		t.Fatalf("normal container status = %s, want %s", got, domain.ContainerLoaded)
	}
}

// TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange covers the same
// cargo after its gateway comes online with an in-range reading.
func TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange(t *testing.T) {
	m := newManifestEnv(t)
	defer m.env.resSvc.Close()

	m.addArrivedCargo(t, "req-battery", domain.CargoTypePowerBattery)
	m.reportTemperature(t, "req-battery", 19.5)

	loaded := m.verifyManifest(t)
	if len(loaded) != 1 || loaded[0] != "req-battery" {
		t.Fatalf("loaded = %v, want [req-battery] once an in-range reading exists", loaded)
	}
	if got := m.containerStatus(t, "req-battery"); got != domain.ContainerLoaded {
		t.Fatalf("container status = %s, want %s", got, domain.ContainerLoaded)
	}
}

// TestHTTP_ManifestHoldsTemperatureCargoOutOfRange pins the out-of-range case.
func TestHTTP_ManifestHoldsTemperatureCargoOutOfRange(t *testing.T) {
	m := newManifestEnv(t)
	defer m.env.resSvc.Close()

	m.addArrivedCargo(t, "req-battery", domain.CargoTypePowerBattery)
	m.reportTemperature(t, "req-battery", 31.0)

	loaded := m.verifyManifest(t)
	if len(loaded) != 0 {
		t.Fatalf("loaded = %v, want none for cargo reporting 31.0C", loaded)
	}
	if got := m.containerStatus(t, "req-battery"); got != domain.ContainerArrived {
		t.Fatalf("container status = %s, want it to stay %s", got, domain.ContainerArrived)
	}
}

// TestTemperatureSweepReportsCargoWithoutAnyReading covers the background
// monitoring view of the same cargo: a temperature-controlled container that
// never reported must be flagged both as overdue and as not compliant, so the
// two views of the same cargo cannot contradict each other.
func TestTemperatureSweepReportsCargoWithoutAnyReading(t *testing.T) {
	m := newManifestEnv(t)
	defer m.env.resSvc.Close()

	m.addArrivedCargo(t, "req-battery", domain.CargoTypePowerBattery)

	alerts := m.env.delSvc.CheckTemperatureCompliance(time.Now().UTC())

	overdue := false
	outOfRange := false
	for _, a := range alerts {
		if !strings.Contains(a, "c-req-battery") {
			continue
		}
		if strings.Contains(a, "overdue") {
			overdue = true
		}
		if strings.Contains(a, "out of range") {
			outOfRange = true
		}
	}
	if !overdue {
		t.Fatalf("alerts = %v, want an overdue-reporting alert", alerts)
	}
	if !outOfRange {
		t.Fatalf("alerts = %v, want cargo without any reading to also count as not compliant", alerts)
	}
}
