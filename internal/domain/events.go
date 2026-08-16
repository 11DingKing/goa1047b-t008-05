package domain

import "time"

// Event is the marker interface for all domain events.
type Event interface {
	EventName() string
}

// SlotLockedEvent is emitted when a reservation successfully locks a slot.
type SlotLockedEvent struct {
	ReservationID string
	SlotID        string
	WindowID      string
	OwnerID       string
	CargoType     CargoType
	OccurredAt    time.Time
}

func (e SlotLockedEvent) EventName() string { return "slot.locked" }

// SlotReleasedEvent is emitted when a slot is released (auto or manual).
type SlotReleasedEvent struct {
	ReservationID string
	SlotID        string
	Reason        string
	OccurredAt    time.Time
}

func (e SlotReleasedEvent) EventName() string { return "slot.released" }

// SlotFrozenEvent is emitted when a slot is frozen due to disruption.
type SlotFrozenEvent struct {
	SlotID     string
	Reason     string
	OccurredAt time.Time
}

func (e SlotFrozenEvent) EventName() string { return "slot.frozen" }

// CargoArrivedEvent is emitted when a container arrives at the yard.
type CargoArrivedEvent struct {
	ContainerID string
	SlotID      string
	OccurredAt  time.Time
}

func (e CargoArrivedEvent) EventName() string { return "cargo.arrived" }

// TemperatureAlertEvent is emitted when a reading is out of bounds.
type TemperatureAlertEvent struct {
	ContainerID string
	Temperature float64
	OccurredAt  time.Time
}

func (e TemperatureAlertEvent) EventName() string { return "temperature.alert" }

// DeliveryConfirmedEvent is emitted when delivery is confirmed at the destination.
type DeliveryConfirmedEvent struct {
	ReservationID string
	ContainerID   string
	OccurredAt    time.Time
}

func (e DeliveryConfirmedEvent) EventName() string { return "delivery.confirmed" }

// RescheduleEvent is emitted when a reservation is rescheduled.
type RescheduleEvent struct {
	ReservationID string
	FromSlotID    string
	ToSlotID      string
	SuezBackup    bool
	OccurredAt    time.Time
}

func (e RescheduleEvent) EventName() string { return "reservation.rescheduled" }

// WaitlistUpdatedEvent is emitted when a reservation enters or moves in the waitlist.
type WaitlistUpdatedEvent struct {
	WindowID      string
	ReservationID string
	Position      int
	OccurredAt    time.Time
}

func (e WaitlistUpdatedEvent) EventName() string { return "waitlist.updated" }

// ManualInterventionEvent is emitted when a reservation is flagged for manual handling.
type ManualInterventionEvent struct {
	ReservationID string
	Reason        string
	OccurredAt    time.Time
}

func (e ManualInterventionEvent) EventName() string { return "manual.intervention" }
