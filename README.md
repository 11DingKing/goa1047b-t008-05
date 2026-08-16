# Arctic Express Slot Scheduler

A production-grade Go backend for the China–Europe Arctic Express shipping
service. It manages peak-season slot reservations, cargo collection with
temperature monitoring, loading manifest verification, destination-port
delivery confirmation, and exception rescheduling with failure recovery.

## Business Rules

1. **Auto-release**: Cargo that has not arrived at the yard 24 hours before
   the window cutoff is automatically released, and the freed capacity is
   offered to the next waitlisted reservation.
2. **Temperature control**: Energy-storage cabinets and power batteries must
   stay within 15–25 °C and report a reading every 30 minutes.
3. **Priority retention**: A cargo owner's high-value cargo is prioritized
   over normal cargo for slot retention.
4. **Oversell cap**: Peak-window oversell is limited to 10% and requires a
   dispatcher's secondary confirmation.
5. **Reschedule constraints**: Rescheduling is limited to the next voyage on
   the same route. Switching to the Suez backup route requires the cargo
   owner's written consent.

## Architecture

| Package | Responsibility |
|---------|---------------|
| `internal/domain` | Entities, value objects, domain events, errors |
| `internal/store` | Thread-safe in-memory persistence with snapshot |
| `internal/service` | Application orchestration: reservation, delivery, recovery |
| `internal/transport` | HTTP handlers and routing (Go 1.22+ ServeMux) |
| `internal/background` | Periodic scheduler for auto-release and temperature checks |
| `cmd/server` | Entry point, wiring, graceful shutdown |

## Quick Start

### Run locally

```bash
go run ./cmd/server
# Server listens on :59235
```

### Run tests

```bash
go test -timeout=120s -count=1 ./...
```

### Docker

```bash
# Build (auto-detects host architecture: amd64 or arm64)
docker build -t arctic-express-scheduler .

# Run
docker run -p 59235:59235 arctic-express-scheduler

# Multi-arch build for both amd64 and arm64
docker buildx build --platform linux/amd64,linux/arm64 -t arctic-express-scheduler .
```

## Configuration

| Env | Default | Description |
|-----|---------|-------------|
| `PORT` | `59235` | HTTP listen port |

## API Endpoints

Port: **59235**

### Windows (dispatcher)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/windows` | Create a peak delivery window |
| `GET` | `/api/windows` | List all windows |
| `GET` | `/api/windows/{id}/waitlist` | Get waitlist for a window |

### Slots

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/slots` | Create a slot |
| `GET` | `/api/slots` | List all slots |
| `POST` | `/api/slots/{id}/lock` | Lock a slot (cargo owner) |
| `POST` | `/api/slots/{id}/confirm-oversell` | Confirm oversell (dispatcher) |
| `POST` | `/api/slots/{id}/manifest` | Verify loading manifest (document clerk) |
| `POST` | `/api/slots/{id}/freeze` | Freeze slot on disruption (dispatcher) |
| `POST` | `/api/slots/{id}/unfreeze` | Unfreeze a slot |

### Containers (cargo owner, tally clerk)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/containers` | Register a container |
| `POST` | `/api/containers/{id}/arrive` | Record yard arrival |
| `POST` | `/api/containers/{id}/temperature` | Record temperature reading |

### Reservations

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/reservations/{id}` | Get reservation details |
| `POST` | `/api/reservations/{id}/deliver` | Confirm delivery (tally clerk) |
| `POST` | `/api/reservations/{id}/reschedule` | Reschedule to next voyage |

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/health` | Health check |

## Example Usage

```bash
# 1. Create a peak window (capacity 10, 10% oversell)
curl -s -X POST http://localhost:59235/api/windows \
  -H 'Content-Type: application/json' \
  -d '{"id":"win-1","voyage_id":"voy-1","origin_port":"CNYTN","destination_port":"NLRTM","cutoff_time":"2026-09-01T12:00:00Z","start_time":"2026-08-30T12:00:00Z","end_time":"2026-09-02T12:00:00Z","capacity":10,"oversell_ratio":0.1}'

# 2. Create a slot
curl -s -X POST http://localhost:59235/api/slots \
  -H 'Content-Type: application/json' \
  -d '{"id":"slot-1","window_id":"win-1","voyage_id":"voy-1","capacity":10}'

# 3. Register a container
curl -s -X POST http://localhost:59235/api/containers \
  -H 'Content-Type: application/json' \
  -d '{"id":"c1","owner_id":"owner-1","cargo_type":"high_value","destination_port":"NLRTM"}'

# 4. Lock a slot
curl -s -X POST http://localhost:59235/api/slots/slot-1/lock \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"req-1","container_id":"c1","owner_id":"owner-1"}'
```
