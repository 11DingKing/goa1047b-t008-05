package domain

import "errors"

// Domain errors returned by entity methods and services.
var (
	ErrSlotNotFound             = errors.New("slot not found")
	ErrWindowNotFound           = errors.New("window not found")
	ErrContainerNotFound        = errors.New("container not found")
	ErrReservationNotFound      = errors.New("reservation not found")
	ErrSlotFull                 = errors.New("slot at capacity")
	ErrSlotFrozen               = errors.New("slot is frozen")
	ErrInvalidState             = errors.New("invalid state transition")
	ErrOversellUnconfirmed      = errors.New("oversell requires dispatcher confirmation")
	ErrSuezConsentRequired      = errors.New("suez backup route requires written cargo owner consent")
	ErrNotTemperatureControlled = errors.New("cargo type does not require temperature monitoring")
	ErrSameRouteOnly            = errors.New("reschedule only allowed to next voyage on same route")
	ErrSameVoyage               = errors.New("cannot reschedule to the same voyage")
	ErrAutoReleaseTriggered     = errors.New("cargo did not arrive 24h before cutoff; slot auto-released")
)
