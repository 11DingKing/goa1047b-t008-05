# syntax=docker/dockerfile:1

# ---- Build stage ----
# Go 1.26 on Debian Bookworm; supports amd64 and arm64 via buildx.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -trimpath -o /out/server ./cmd/server

# ---- Runtime stage ----
# Minimal scratch image containing only the static binary.
FROM scratch

COPY --from=builder /out/server /server

EXPOSE 59235

ENTRYPOINT ["/server"]
