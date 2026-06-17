# deej-x — Project Context (SERENITY)

## Standing Rules for Claude

- **Hardware questions go to the firmware CLAUDE.md.** All hardware details (pin assignments, display wiring, EEPROM layout, encoder behavior, RGB button state, etc.) are documented in `C:\Users\Steven\Documents\Solid Models\deej\SERENITY Firmware\SERENITY-Firmware\CLAUDE.md`. Do not assume hardware details not documented here or there.
- **TBD items are blockers.** Several protocol details are explicitly deferred (icon format, icon dimensions, bitmap bit order, HID report format). Do not guess or assume values for these — check the TBD section first and ask the user if a decision is needed before proceeding.
- **Windows is the primary target.** Linux is best-effort secondary. All features must work on Windows. Linux implementations follow the same OS-abstraction pattern already established in the codebase.
- **This is a personal fork, not an upstream PR.** Do not attempt to maintain compatibility with the vanilla deej serial protocol or configuration format beyond what is documented here.

---

## Overview

Fork of [omriharel/deej](https://github.com/omriharel/deej) (MIT license), a Go desktop app that reads slider values from a serial-connected microcontroller and maps them to Windows/Linux audio session volumes.

**Module path:** `github.com/sclead03/deej-x`

This fork is customized exclusively for the **SERENITY** hardware: a custom ATmega32U4-based audio mixer with 5 faders, 5 mute buttons/LEDs, a rotary encoder, an RGB LED button, and 6 SSD1306 OLED displays. The original deej host app works with SERENITY as-is for basic fader control; this fork adds bidirectional communication, OLED icon/name streaming, and system mic mute via HID.

---

## Differences from Upstream deej

| | Upstream deej | This fork |
|---|---|---|
| Baud rate | 9600 | 115200 |
| Serial output | Continuous stream | Event-driven (on change only) |
| Channel count | Variable | 6 (index 0 = master vol encoder, 1–5 = faders) |
| Serial direction | Device → host only | Bidirectional |
| HID handling | None | Mic mute via custom HID report |
| Display support | None | Icon + name streaming to 5 channel OLEDs |

### Serial input format (device → host)

```
masterVol|fader0|fader1|fader2|fader3|fader4\r\n
```

Six pipe-delimited values, 0–1023 each. Index 0 is the master volume encoder; indices 1–5 are the analog faders. The existing `expectedLinePattern` regex in `serial.go` handles variable channel counts and requires no changes. The firmware only transmits on value changes — the host must not assume a regular update rate.

Index 0 maps in `config.yaml` like any other channel (`slider_mapping: 0: master`). The host has no special awareness that it is encoder-sourced vs. ADC-sourced.

### Config keys

| Key | Status | Notes |
|---|---|---|
| `com_port` | ✓ existing | |
| `baud_rate` | ✓ updated | Default changed to 115200 |
| `slider_mapping` | ✓ existing | 6-channel SERENITY layout in default config |
| `channel_names` | ✓ implemented | List of 5 strings for channel OLEDs 1–5 |
| `icon_path` | pending | Path to icon source files; add when icon package is implemented |
| `icon_conversion` | pending | `"threshold"` or `"dither"`; add when icon package is implemented |

---

## Feature Status

### ✓ 1. Bidirectional Serial Protocol — COMPLETE

All host → firmware commands use binary framing:

```
[0x00][CMD_ID][LEN_LO][LEN_HI][...payload bytes...]
```

- `0x00` (null byte) is the escape prefix — never appears in ASCII fader data, unambiguous
- `LEN` is payload length in bytes, little-endian 16-bit
- **Fire-and-forget** — no ACK, no retry; USB CDC serial reliability is sufficient

**Assigned CMD_IDs:**

| Name | CMD_ID | Payload | Description |
|---|---|---|---|
| `CMD_QUERY` | `0x01` | none | Host → firmware: request ready beacon |
| `SET_CHANNEL_NAME` | `0x02` | `[channel_idx][name\0]` | Push display name for channel N |
| `SET_CHANNEL_ICON` | `0x03` | `[channel_idx][bitmap bytes]` | Push icon bitmap for channel N |

Implemented in `serial_writer.go`. `SerialWriter` is created by `SerialIO` on connect and exposed via `SerialIO.Writer()`.

### ✓ 2. Connection Handshake and Push Trigger — COMPLETE

Both connection scenarios are handled:

**Device connects while host is already running:**
- Firmware sends `SERENITY\r\n` after its 1500ms startup delay
- `serial.go` detects this line before the fader pattern check, notifies beacon consumers

**Host launches with device already connected:**
- `display.go` receives the connect event, sends `CMD_QUERY` immediately
- Firmware responds with `SERENITY\r\n`
- Beacon received → push triggered

**Manual trigger:** "Push display icons" tray menu item calls `display.TriggerPush()`, skipping unchanged channels.

**Host-side change detection:** `DisplayManager` tracks `lastSentNames` and `lastSentIcons` per channel. Connection events force-push all channels; manual pushes skip unchanged ones.

Implemented in `display.go` and `serial.go` (connect/beacon pub-sub channels).

### ✓ 3. Channel Name Streaming — COMPLETE

Names are pushed via `SET_CHANNEL_NAME` on every connection event and on manual push.

- Source: `channel_names` list in `config.yaml`, read into `CanonicalConfig.ChannelNames [5]string`
- `MaxChannelNameLength = 15` (constant in `serial_writer.go`; revisit when firmware font size is finalized)
- Config reload automatically picks up new names on the next manual push

Icon streaming is **pending** — see TBD section.

### ✓ 4. System Mic Mute via HID — COMPLETE (report validation pending TBD)

**Device identification:**
- USB VID: `0x1209`, USB PID: `0x0001`
- SERENITY enumerates as a composite USB device: CDC serial + HID

**HID implementation (pure Go, no CGO):**
- Windows: enumerates HID devices via `setupapi.dll` and `hid.dll` using `syscall.NewLazyDLL` — no C compiler required
- Device matched by VID/PID string in the device path, opened with `CreateFile`, read with `ReadFile`
- `MicMuter` interface with `_windows.go` / `_linux.go` implementations
- Windows mic mute: WASAPI/MMDeviceAPI (`go-wca`, already a dependency) to toggle default recording device mute
- Linux mic mute: `pactl set-source-mute @DEFAULT_SOURCE@ toggle` (best-effort)
- Linux HID enumeration: not yet implemented (`openSERENITY` returns an error; HID manager retries silently)

**Pending:** `handleReport` in `hid.go` currently triggers mute on any received report. Once the firmware HID descriptor is finalized, add a report format check there.

---

## Codebase Structure

```
pkg/deej/
  cmd/main.go                  — entry point
  deej.go                      — main Deej struct, lifecycle
  config.go                    — config loading (viper)
  serial.go                    — serial I/O, fader parsing, connect/beacon events
  serial_writer.go             — host → firmware command framing (binary protocol)
  display.go                   — handshake, name push sequencing, change tracking
  hid.go                       — HIDManager, MicMuter interface, read loop
  hid_windows.go               — Win32 HID enumeration + WASAPI mic mute
  hid_linux.go                 — Linux stubs (HID enumeration TBD, mic mute via pactl)
  session.go                   — audio session abstraction
  session_map.go               — slider → session mapping
  session_finder.go            — interface
  session_finder_windows.go
  session_finder_linux.go
  tray.go                      — system tray (includes "Push display icons" item)
  notify.go                    — desktop notifications
  logger.go
  panic.go                     — crash handler
  util/util.go
  util/util_windows.go
  util/util_linux.go
  icon/icon.go                 — tray/notification icon data (generated, do not edit)

  icon/                        — TO BE CREATED
    icon.go                    — icon loading, conversion (threshold + dither), fallback X generation
```

---

## Remaining Work

### Icon Package (`pkg/deej/icon/` — new package, separate from existing `icon/icon.go`)

Blocked on firmware TBDs. Once unblocked:

1. Add `icon_path` and `icon_conversion` keys to `config.go` and `config.yaml`
2. Implement `pkg/deej/icon/icon.go`:
   - Load source images from `icon_path` (format TBD)
   - Convert to 1-bit bitmap: threshold (clean line art) or Floyd-Steinberg dither (grayscale)
   - Generate fallback X bitmap if no source file found
3. Wire into `display.go` `pushAll()` — the icon push TODO is already marked there

### HID Report Validation

One-line fill-in once firmware HID descriptor is known. See `handleReport` in `hid.go`.

### Linux HID Enumeration

Best-effort. Implement `openSERENITY()` in `hid_linux.go` by enumerating `/dev/hidraw*` and matching VID/PID via `/sys/class/hidraw/<dev>/device/uevent`.

---

## OS Support

| Feature | Windows | Linux |
|---|---|---|
| Fader/serial reading | ✓ | ✓ |
| Bidirectional serial | ✓ | ✓ |
| Channel name streaming | ✓ | ✓ |
| Icon streaming | pending TBD | pending TBD |
| HID device reading | ✓ | stub (retries silently) |
| Mic mute toggle | ✓ (WASAPI) | best-effort (pactl) |

---

## TBD — Do Not Assume These

| Item | Blocked on |
|---|---|
| Icon file format | User decision (format that produces best dot-matrix quality) |
| Icon file naming convention | Format decision |
| Icon dimensions (px) | Firmware font size / display layout finalization |
| Bitmap bit order for SET_CHANNEL_ICON | Firmware direct SSD1306 write implementation |
| Custom HID report format (mic mute) | RGB button hardware replacement + firmware HID descriptor |

---

## Reference

- Firmware repo and hardware ground truth: `C:\Users\Steven\Documents\Solid Models\deej\SERENITY Firmware\SERENITY-Firmware\CLAUDE.md`
- Upstream deej: https://github.com/omriharel/deej
