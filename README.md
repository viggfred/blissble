# blissble

[![CI](https://github.com/viggfred/blissble/actions/workflows/ci.yml/badge.svg)](https://github.com/viggfred/blissble/actions/workflows/ci.yml)

Control **Hunter Douglas "Bliss Smart Blinds"** motors directly over **Bluetooth Low Energy (BLE)** from Go — no hub, no cloud account, no vendor app.

This is a clean-room reimplementation of the BLE protocol used by the official
**Bliss Smart Blinds** app (`nl.hunterdouglas.bliss`, iOS/Android), reverse-engineered
from the app. It talks to the motor directly using its published GATT service.

> **Not** the same as "BLISS Automation by Alta" (which is Coulisse **MotionBlinds** and
> uses a completely different, encrypted, Wi‑Fi‑bridge protocol — see
> [motionblindsble](https://github.com/LennP/motionblindsble)). This library is for the
> Hunter Douglas Europe **Bliss Smart Blinds** product only.

### Supported motors

Motors that advertise a BLE name like `HD1300` (also `HD1000/1001/1200`, `HD3600/3700/3800/3900`,
Tuiss `TS…`) and expose GATT service `00010203-0405-0607-0809-0a0b0c0d1910`.
Developed and validated against an **HD1300**. Other models very likely work (same command set);
reports welcome.

## Install

```sh
go install github.com/viggfred/blissble/cmd/blissctl@latest
```

Or add the library to your project:

```sh
go get github.com/viggfred/blissble/pkg/bliss
```

Requires Linux/BlueZ, macOS, or Windows (via [`tinygo.org/x/bluetooth`](https://github.com/tinygo-org/bluetooth)).
On Linux you need BlueZ running; no root is required.

## CLI

```sh
blissctl -mac AA:BB:CC:DD:EE:01        # or set BLISS_MAC
```

```
Connected and logged in. Type 'help' for commands, 'quit' to exit.
blissble> open
blissble> pos 50
blissble> slow down                       # fine/precision nudge
blissble> speed 50                         # motor speed preset (25/50/75/100)
blissble> fav                              # go to saved favorite (setfav / delfav)
blissble> clock                            # sync the motor clock (needed for schedules)
blissble> timer add 1 07:30 100 weekdays   # open fully at 07:30 Mon–Fri
blissble> timer slots                      # next free slot; timer del <n> / timer clear
blissble> stop
blissble> status
blissble> quit
```

Find your motor's MAC with `bluetoothctl scan on` (look for an `HD…` device).

## Library

```go
package main

import (
	"context"
	"log"

	"github.com/viggfred/blissble/pkg/bliss"
)

func main() {
	blind := bliss.New(bliss.Config{MACAddress: "AA:BB:CC:DD:EE:01"})
	blind.OnEvent(func(ev bliss.Event) {
		if ev.Type == bliss.EventStatus {
			log.Printf("position=%d battery=%s", ev.Position, ev.Battery)
		}
	})

	if err := blind.Connect(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer blind.Disconnect()

	blind.Open()               // raise
	blind.SetPosition(50)      // go to 50%
	blind.Stop()
	blind.RequestStatus()
}
```

## Home Assistant (blissha)

`blissha` is a small daemon that bridges any number of blinds to Home Assistant
over MQTT. It uses [MQTT discovery](https://www.home-assistant.io/integrations/mqtt/),
so each blind shows up automatically as one HA **device** with:

- a **Cover** — open / close / stop and a 0–100 position slider
- **Buttons** — *Fast up/down* (full speed) and *Slow up/down* (fine step)
- a **Battery** diagnostic sensor (normal / low / none)

Standard Home Assistant cover controls just work — no custom dashboard needed.

### Configure

Copy [`cmd/blissha/config.example.yaml`](cmd/blissha/config.example.yaml) and edit:

```yaml
mqtt:
  broker: tcp://192.168.1.10:1883
  username: mqtt_user
  password: mqtt_pass
poll_interval: 30s
blinds:
  - name: Living Room
    mac: AA:BB:CC:DD:EE:01
    device_class: shade
    # invert: true    # set if open/closed end up swapped in HA
  - name: Bedroom
    mac: AA:BB:CC:DD:EE:FF
```

**Multiple adapters (multi-room).** BLE range is short, so for blinds in
different rooms you can use one USB Bluetooth dongle per room (e.g. on a USB
extension). Set each blind's `adapter:` to that dongle's own Bluetooth MAC —
stable across reboots, unlike `hciN` numbering. Blinds on the same adapter
serialize their scans; different adapters scan in parallel. Omit `adapter:` to
use the default (`hci0`). BlueZ handles the multiple adapters automatically.

### Power saving

The motor is a battery device, so BLE activity matters. Two facts shape the
options:

- **Holding a BLE connection open — not the polling rate — is the main battery
  cost.** A connected peripheral must service the link continuously; a status
  read over an already-open link is almost free.
- **Position is only readable by connecting.** The motor advertises ~every
  500&nbsp;ms while idle, but its advertisement carries only static metadata
  (firmware, limit flags) — *not* position or battery (verified on-device). So
  there is no way to read state passively, and changes made with the RF remote
  can't be observed until `blissha` next connects.

Given that, pick a mode with two knobs — `idle_disconnect` and `poll_interval`:

| Mode | Config | Behaviour | Battery |
|------|--------|-----------|---------|
| **Persistent** (default) | `idle_disconnect` unset | Holds the link open, polls every `poll_interval` (default 30s) | Highest |
| **On-demand** | `idle_disconnect: 30s` | Connects only for a command or a refresh, drops the link after the idle window (default `poll_interval` 1h) | Low |
| **Command-only** | `idle_disconnect: 30s`, `poll_interval: 0` | Connects *only* when HA sends a command; never polls | Lowest |

Command-only (`poll_interval: 0`) is the most frugal: HA stays accurate for
HA-driven moves and only goes stale after RF-remote/app use — which can't be
detected cheaply anyway. Commands take a few extra seconds (scan + connect),
which is fine for scenes and automations. `blissha` still does one status read
at startup and retries a briefly-unreachable blind on a short backoff.

### Run (podman / docker)

The bridge talks to the host's BlueZ over the system D-Bus socket, so mount that
socket and run the container as root:

```sh
podman build -t blissble -f Containerfile .
podman run -d --name blissha \
  -v /run/dbus/system_bus_socket:/run/dbus/system_bus_socket:ro \
  -v ./config.yaml:/config/config.yaml:ro \
  blissble
```

`docker` works the same way, and there's a [`compose.yaml`](compose.yaml) for
Docker/Podman Compose. If BLE scanning misbehaves in a restricted network
namespace, add `--net=host`.

## Layout

```
cmd/blissctl     interactive CLI
cmd/blissha      Home Assistant MQTT bridge (container)
pkg/bliss        importable library
  protocol.go    pure command builders + response parser (no BLE deps, unit-tested)
  client.go      BLE transport (scan → connect → login → commands) via tinygo bluetooth
```

The wire-format logic in `protocol.go` has no Bluetooth dependency, so it is fully
unit-tested (`go test ./pkg/bliss`) and reusable if you want to port the protocol to
another transport or language.

## Protocol notes

Reverse-engineered from the official app and validated on-device. Everything is
**plaintext** — there is no encryption and no per-device key.

**GATT (service `00010203-0405-0607-0809-0a0b0c0d1910`)**

| Role     | Characteristic                          | Properties |
|----------|-----------------------------------------|------------|
| Command  | `00010405-0405-0607-0809-0a0b0c0d1910`  | write (with response) |
| Response | `00010304-0405-0607-0809-0a0b0c0d1910`  | notify |

(The motor is an nRF52 and also exposes a Nordic DFU service `0000fe59` — do **not** write to it.)

**Flow:** scan → connect → subscribe to Response → send **login** → send commands.
The device rejects commands until a successful login.

**Login** — `FF 03 03 03 03` + password bytes (min 6, zero-padded). The app ships a
universal hard-coded password `xxxxxx`, so login is `FF 03 03 03 03 78 78 78 78 78 78`.

**Commands** (written to the Command characteristic):

| Action         | Bytes                                                             |
|----------------|-------------------------------------------------------------------|
| Up / open      | `FF 58 EA 41 CF 03 01`                                            |
| Down / close   | `FF 58 EA 41 1F 03 01`                                            |
| Stop           | `FF 58 EA 41 5F 03 01`                                            |
| Fine up / down | `FF 58 EA 41 22 03 01` / `FF 58 EA 41 23 03 01` (slow step)       |
| Go to position | `FF 58 EA 41 BF 03` + position                                    |
| Speed preset   | `FF 58 EA 41 F0/F1/F2/F3 03 01` (100/75/50/25 %)                  |
| Favorite       | `FF 58 EA 41 93` go · `91` save · `92` delete                     |
| Read status    | `FF 58 EA 41 D1 03 01`                                            |
| Heartbeat      | `FF 01 01 01 01 01 01`                                            |
| Set clock      | `FF 58 EA 41 02 00` + `yy mm dd hh mm ss`                         |
| Add timer      | `FF 58 EA 41 03 <silent> <slot> B2 3F <dayMask> hh mm ss` + position |
| Delete timer   | `FF 58 EA 41 03 01 <slot>`                                        |
| Query slots    | `FF 58 EA 41 04`                                                  |

Position is scaled to the motor range (1000 on HD1300) and sent as a 16-bit
little-endian value (e.g. 50% → 500 → `F4 01`); motors with range 100 use a single byte.

Schedules use 16 slots (1–16). `dayMask` is a weekday bitmask —
`Sun=0x01, Mon=0x02, Tue=0x04, Wed=0x08, Thu=0x10, Fri=0x20, Sat=0x40` (all = `0x7F`);
`silent` = `0x80` suppresses the audible confirmation. The trailing position bytes
encode the target the same way as *Go to position* (single-bar motors like HD1300).

**Responses** (notifications) have header `FF 01 02 03` + opcode + payload:

| Opcode | Meaning        | Payload |
|--------|----------------|---------|
| `D4`   | login result   | byte[2] > 0 ⇒ success |
| `D3`   | password set   | byte[2] > 0 ⇒ success |
| `D1` / `D2` | status (readStatus reply / pushed report — same layout) | byte[1]=flags, byte[2]=position %, byte[3..4]=raw position (LE); `flags & 0x18`: `0x00`=battery normal, `0x08`=low, `0x10`=none; bit0=reverse-config (not live direction), bit1=limit-set, bit2=remote-link |
| `D6`   | next free timer slot | byte[2]=slot index |
| `D7`   | add-timer result | byte[2] > 0 ⇒ success |
| `D8`   | delete-timer result | byte[2] > 0 ⇒ success |

## Development

```sh
make tools     # install the pinned golangci-lint (once)
make check     # build + vet + lint + test (what CI runs)
make fmt       # gofmt + goimports
make test      # go test -race ./...
make bump BUMP=minor   # tag the next release (patch|minor|major); then git push --tags
```

CI (GitHub Actions, `.github/workflows/ci.yml`) runs build/vet/test and lint on
every push and PR. Public repos get unlimited Actions minutes, so there's no
usage concern here.

## Security & privacy

The Bliss BLE protocol has **no meaningful authentication or encryption**: the
login "password" is a fixed value baked into the app and shared across units,
and every frame is plaintext. In effect, anyone within Bluetooth range can
control the blind and read its status. That's a property of the device, not this
library — this tool doesn't weaken anything, but it also can't add security the
motor doesn't have. Keep it in mind before relying on these blinds for privacy.

## Disclaimer

Independent, unofficial project. Not affiliated with or endorsed by Hunter Douglas.
Protocol details were derived by inspecting the official app for interoperability with
hardware the user owns. Use at your own risk.

## License

[MIT](LICENSE)
