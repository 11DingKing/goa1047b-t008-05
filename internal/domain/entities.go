package domain

import "time"

// CargoType represents the category of cargo being shipped on the Arctic Express service.
type CargoType string

const (
	CargoTypeNormal        CargoType = "normal"
	CargoTypeHighValue     CargoType = "high_value"
	CargoTypeEnergyStorage CargoType = "energy_storage"
	CargoTypePowerBattery  CargoType = "power_battery"
)

// Priority returns a numeric priority used for waitlist sorting; higher wins.
func (c CargoType) Priority() int {
	switch c {
	case CargoTypeHighValue:
		return 3
	case CargoTypeEnergyStorage, CargoTypePowerBattery:
		return 2
	default:
		return 1
	}
}

// IsTemperatureControlled reports whether the cargo type requires continuous
// temperature monitoring (energy storage cabinets and power batteries).
func (c CargoType) IsTemperatureControlled() bool {
	return c == CargoTypeEnergyStorage || c == CargoTypePowerBattery
}

// SlotStatus represents the lifecycle state of a voyage slot.
type SlotStatus string

const (
	SlotStatusAvailable SlotStatus = "available"
	SlotStatusLocked    SlotStatus = "locked"
	SlotStatusFrozen    SlotStatus = "frozen"
	SlotStatusReleased  SlotStatus = "released"
	SlotStatusLoaded    SlotStatus = "loaded"
	SlotStatusDelivered SlotStatus = "delivered"
)

// ReservationStatus represents the lifecycle state of a reservation.
type ReservationStatus string

const (
	ReservationPending     ReservationStatus = "pending"
	ReservationLocked      ReservationStatus = "locked"
	ReservationQueued      ReservationStatus = "queued"
	ReservationLoaded      ReservationStatus = "loaded"
	ReservationDelivered   ReservationStatus = "delivered"
	ReservationReleased    ReservationStatus = "released"
	ReservationRescheduled ReservationStatus = "rescheduled"
	ReservationRolled      ReservationStatus = "rolled"
)

// ContainerStatus represents the lifecycle state of a container.
type ContainerStatus string

const (
	ContainerRegistered ContainerStatus = "registered"
	ContainerArrived    ContainerStatus = "arrived"
	ContainerLoaded     ContainerStatus = "loaded"
	ContainerDelivered  ContainerStatus = "delivered"
	ContainerRolled     ContainerStatus = "rolled"
)

// Temperature bounds and reporting interval for temperature-controlled cargo.
const (
	MinTempC           = 15.0
	MaxTempC           = 25.0
	TempReportInterval = 30 * time.Minute
	AutoReleaseWindow  = 24 * time.Hour
)

// PeakWindow represents a peak-season delivery window on a specific voyage.
type PeakWindow struct {
	ID              string    `json:"id"`
	VoyageID        string    `json:"voyage_id"`
	OriginPort      string    `json:"origin_port"`
	DestinationPort string    `json:"destination_port"`
	CutoffTime      time.Time `json:"cutoff_time"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	Capacity        int       `json:"capacity"`
	OversellRatio   float64   `json:"oversell_ratio"`
}

// EffectiveCapacity returns the maximum bookable capacity including oversell.
func (w PeakWindow) EffectiveCapacity(oversellConfirmed bool) int {
	if !oversellConfirmed {
		return w.Capacity
	}
	return w.Capacity + int(float64(w.Capacity)*w.OversellRatio)
}

// IsSameRoute returns true when another window serves the same origin/destination pair.
func (w PeakWindow) IsSameRoute(other PeakWindow) bool {
	return w.OriginPort == other.OriginPort && w.DestinationPort == other.DestinationPort
}

// Slot represents bookable capacity on a voyage within a peak window.
type Slot struct {
	ID                string     `json:"id"`
	WindowID          string     `json:"window_id"`
	VoyageID          string     `json:"voyage_id"`
	Capacity          int        `json:"capacity"`
	Booked            int        `json:"booked"`
	Status            SlotStatus `json:"status"`
	Version           int        `json:"version"`
	OversellConfirmed bool       `json:"oversell_confirmed"`
	Frozen            bool       `json:"frozen"`
	FrozenReason      string     `json:"frozen_reason,omitempty"`
	SuezBackup        bool       `json:"suez_backup,omitempty"`
}

// CanBook reports whether the slot has available capacity for the given window.
func (s *Slot) CanBook(w PeakWindow) bool {
	if s.Frozen {
		return false
	}
	return s.Booked < w.EffectiveCapacity(s.OversellConfirmed)
}

// NeedsOversell reports whether the next booking would require oversell capacity.
func (s *Slot) NeedsOversell(w PeakWindow) bool {
	return s.Booked >= w.Capacity && s.Booked < w.EffectiveCapacity(true)
}

// Book increments the booked count and transitions to locked if available.
func (s *Slot) Book() {
	s.Booked++
	if s.Status == SlotStatusAvailable {
		s.Status = SlotStatusLocked
	}
	s.Version++
}

// Release decrements the booked count and may transition back to available.
func (s *Slot) Release() {
	if s.Booked > 0 {
		s.Booked--
	}
	if s.Booked == 0 {
		s.Status = SlotStatusAvailable
	}
	s.Version++
}

// Freeze marks the slot as frozen due to a disruption.
func (s *Slot) Freeze(reason string) {
	s.Frozen = true
	s.FrozenReason = reason
	s.Status = SlotStatusFrozen
	s.Version++
}

// Unfreeze removes the frozen state and restores the previous logical status.
func (s *Slot) Unfreeze() {
	s.Frozen = false
	s.FrozenReason = ""
	if s.Booked > 0 {
		s.Status = SlotStatusLocked
	} else {
		s.Status = SlotStatusAvailable
	}
	s.Version++
}

// Load transitions the slot to loaded status.
func (s *Slot) Load() {
	s.Status = SlotStatusLoaded
	s.Version++
}

// Deliver transitions the slot to delivered status.
func (s *Slot) Deliver() {
	s.Status = SlotStatusDelivered
	s.Version++
}

// ConfirmOversell enables oversell capacity for the slot.
func (s *Slot) ConfirmOversell() {
	s.OversellConfirmed = true
	s.Version++
}

// TempReading represents a single temperature reading from a container.
type TempReading struct {
	Timestamp   time.Time `json:"timestamp"`
	Temperature float64   `json:"temperature"`
}

// Container represents a shipping container with its cargo metadata.
type Container struct {
	ID              string          `json:"id"`
	OwnerID         string          `json:"owner_id"`
	CargoType       CargoType       `json:"cargo_type"`
	DestinationPort string          `json:"destination_port"`
	Status          ContainerStatus `json:"status"`
	ArrivedAt       *time.Time      `json:"arrived_at,omitempty"`
	Readings        []TempReading   `json:"readings,omitempty"`
	LoadedAt        *time.Time      `json:"loaded_at,omitempty"`
	DeliveredAt     *time.Time      `json:"delivered_at,omitempty"`
}

// RecordArrival marks the container as arrived at the loading port yard.
func (c *Container) RecordArrival(t time.Time) error {
	if c.Status != ContainerRegistered {
		return ErrInvalidState
	}
	c.Status = ContainerArrived
	c.ArrivedAt = &t
	return nil
}

// RecordTemperature adds a temperature reading for a temperature-controlled container.
func (c *Container) RecordTemperature(t time.Time, temp float64) error {
	if !c.CargoType.IsTemperatureControlled() {
		return ErrNotTemperatureControlled
	}
	c.Readings = append(c.Readings, TempReading{Timestamp: t, Temperature: temp})
	return nil
}

// IsTemperatureCompliant reports whether the latest reading is within bounds.
func (c *Container) IsTemperatureCompliant() bool {
	if len(c.Readings) == 0 {
		return true
	}
	last := c.Readings[len(c.Readings)-1]
	return last.Temperature >= MinTempC && last.Temperature <= MaxTempC
}

// IsReportingOverdue reports whether the last reading is older than the reporting interval.
func (c *Container) IsReportingOverdue(now time.Time) bool {
	if len(c.Readings) == 0 {
		return true
	}
	last := c.Readings[len(c.Readings)-1]
	return now.Sub(last.Timestamp) > TempReportInterval
}

// Load marks the container as loaded onto the vessel.
func (c *Container) Load(t time.Time) error {
	if c.Status != ContainerArrived {
		return ErrInvalidState
	}
	c.Status = ContainerLoaded
	c.LoadedAt = &t
	return nil
}

// Deliver marks the container as delivered at the destination terminal.
func (c *Container) Deliver(t time.Time) error {
	if c.Status != ContainerLoaded {
		return ErrInvalidState
	}
	c.Status = ContainerDelivered
	c.DeliveredAt = &t
	return nil
}

// Roll marks the container as rolled (bumped from the vessel).
func (c *Container) Roll() {
	c.Status = ContainerRolled
}

// Reservation represents a booking of slot capacity for a container.
type Reservation struct {
	ID            string            `json:"id"`
	SlotID        string            `json:"slot_id"`
	WindowID      string            `json:"window_id"`
	ContainerID   string            `json:"container_id"`
	OwnerID       string            `json:"owner_id"`
	CargoType     CargoType         `json:"cargo_type"`
	Status        ReservationStatus `json:"status"`
	Priority      int               `json:"priority"`
	CreatedAt     time.Time         `json:"created_at"`
	LockedAt      *time.Time        `json:"locked_at,omitempty"`
	QueuePosition int               `json:"queue_position,omitempty"`
	SuezConsent   bool              `json:"suez_consent,omitempty"`
	ManualFlag    bool              `json:"manual_flag,omitempty"`
}

// Lock transitions the reservation to locked.
func (r *Reservation) Lock(t time.Time) {
	r.Status = ReservationLocked
	r.LockedAt = &t
}

// Enqueue sets the reservation to queued with a waitlist position.
func (r *Reservation) Enqueue(position int) {
	r.Status = ReservationQueued
	r.QueuePosition = position
}

// Load transitions the reservation to loaded.
func (r *Reservation) Load() {
	r.Status = ReservationLoaded
}

// Deliver transitions the reservation to delivered.
func (r *Reservation) Deliver() {
	r.Status = ReservationDelivered
}

// Release transitions the reservation to released.
func (r *Reservation) Release() {
	r.Status = ReservationReleased
}

// Reschedule transitions the reservation to rescheduled and then locked.
func (r *Reservation) Reschedule(t time.Time) {
	r.Status = ReservationRescheduled
	r.LockedAt = &t
}
