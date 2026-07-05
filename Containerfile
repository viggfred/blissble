# Build and package blissha, the Home Assistant MQTT bridge.
# Works with both `podman build` and `docker build`.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure-Go (no cgo): the Linux BLE backend talks to BlueZ over D-Bus.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/blissha ./cmd/blissha

FROM gcr.io/distroless/static-debian12
# godbus looks here for the system bus; mount the host socket to this path.
ENV DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket
COPY --from=build /out/blissha /usr/local/bin/blissha
ENTRYPOINT ["/usr/local/bin/blissha", "-config", "/config/config.yaml"]
