# blissble

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

## Layout

```
cmd/blissctl     interactive CLI
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

| Action        | Bytes                                   |
|---------------|-----------------------------------------|
| Up / open     | `FF 58 EA 41 CF 03 01`                  |
| Down / close  | `FF 58 EA 41 1F 03 01`                  |
| Stop          | `FF 58 EA 41 5F 03 01`                  |
| Go to position| `FF 58 EA 41 BF 03` + position          |
| Read status   | `FF 58 EA 41 D1 03 01`                  |
| Heartbeat     | `FF 01 01 01 01 01 01`                  |

Position is scaled to the motor range (1000 on HD1300) and sent as a 16-bit
little-endian value (e.g. 50% → 500 → `F4 01`); motors with range 100 use a single byte.

**Responses** (notifications) have header `FF 01 02 03` + opcode + payload:

| Opcode | Meaning        | Payload |
|--------|----------------|---------|
| `D4`   | login result   | byte[2] > 0 ⇒ success |
| `D3`   | password set   | byte[2] > 0 ⇒ success |
| `D2`   | status report  | byte[1]=flags, byte[2]=position; `flags & 0x18`: 0=battery ok, 0x10=low, 0x18=none; bit0=direction, bit1=limit-setting, bit2=remote-link |

## Disclaimer

Independent, unofficial project. Not affiliated with or endorsed by Hunter Douglas.
Protocol details were derived by inspecting the official app for interoperability with
hardware the user owns. Use at your own risk.

## License

[MIT](LICENSE)
