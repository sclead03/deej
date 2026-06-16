# deej Fork — Project Context (SERENITY)

## Standing Rules for Claude

- **Hardware questions go to the firmware CLAUDE.md.** All hardware details (pin assignments, display wiring, EEPROM layout, encoder behavior, RGB button state, etc.) are documented in `C:\Users\Steven\Documents\Solid Models\deej\SERENITY Firmware\SERENITY-Firmware\CLAUDE.md`. Do not assume hardware details not documented here or there.
- **TBD items are blockers.** Several protocol details are explicitly deferred (icon format, icon dimensions, bitmap bit order, HID report format). Do not guess or assume values for these — check the TBD section first and ask the user if a decision is needed before proceeding.
- **Windows is the primary target.** Linux is best-effort secondary. All features must work on Windows. Linux implementations follow the same OS-abstraction pattern already established in the codebase.
- **This is a personal fork, not an upstream PR.** Do not attempt to maintain compatibility with the vanilla deej serial protocol or configuration format beyond what is documented here.

---

## Overview

Fork of [omriharel/deej](https://github.com/omriharel/deej) (MIT license), a Go desktop app that reads slider values from a serial-connected microcontroller and maps them to Windows/Linux audio session volumes.

This fork is customized exclusively for the **SERENITY** hardware: a custom ATmega32U4-based audio mixer with 5 faders, 5 mute buttons/LEDs, a rotary encoder, an RGB LED button, and 6 SSD1306 OLED displays. The original deej host app works with SERENITY as-is for basic fader control; this fork adds bidirectional communication, OLED icon/name streaming, and system mic mute via HID.

---

## Step 0 — Repo Cleanup (Do First)

Before any feature work, audit the repo and remove or flag everything that doesn't belong in this fork. This is a read-and-decide pass, not a blind delete — unknown items should be flagged for discussion before removal.

**Known candidates for removal:**
- `arduino/` directory — contains the vanilla 5-slider Arduino sketch (`deej-5-sliders-vanilla.ino`). SERENITY uses a completely different MCU (ATmega32U4), a different toolchain (PlatformIO), and a separate firmware repo. This sketch has no place here.
- `assets/schematic.png` and `assets/build-*.png/jpg` — reference the original shoebox/breadboard build, not SERENITY hardware.
- `community.md` and `assets/community-builds/` — upstream community showcase content; irrelevant to a personal fork.
- `docs/faq/` — upstream FAQ written for vanilla deej hardware and workflow.
- `.github/FUNDING.yml` — upstream funding config; not applicable.
- CI workflow (`.github/workflows/main.yml`) — may need updating or removal depending on whether CI is wanted for this fork.
- Any "this might be broken" or upstream-TODO comments in Go source files.

**Approach:**
1. Walk the full directory tree and categorize each file/directory: **keep**, **remove**, or **unknown/discuss**.
2. Check Go source files for upstream-specific comments, TODOs, or references that don't apply.
3. Review `config.yaml` — update defaults (baud rate, example slider mapping) to reflect SERENITY.
4. Review `README.md` — upstream README; decide whether to gut it, replace it, or leave a stub.
5. Do not remove anything without confirming with the user if there is any doubt.

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

Six pipe-delimited values, 0–1023 each. Index 0 is the master volume encoder; indices 1–5 are the analog faders. The existing `expectedLinePattern` regex in `serial.go` already handles variable channel counts and requires no changes. The firmware only transmits on value changes — the host must not assume a regular update rate.

Index 0 maps in `config.yaml` like any other channel (`slider_mapping: 0: master`). The host has no special awareness that it is encoder-sourced vs. ADC-sourced.

### Config changes

`config.yaml` default baud rate changes from 9600 to 115200. New fields:

```yaml
# Path to the icon library directory (icons named by channel index)
icon_path: ./icons

# Conversion algorithm for source images to 1-bit bitmaps: "threshold" or "dither"
icon_conversion: threshold
```

The `defaultBaudRate` constant in `config.go` must be updated to 115200.

---

## Planned Features

### 1. Bidirectional Serial Protocol

All host → firmware commands use binary framing:

```
[0x00][CMD_ID][LEN_LO][LEN_HI][...payload bytes...]
```

- `0x00` (null byte) is the escape prefix — never appears in ASCII fader data, unambiguous
- `LEN` is payload length in bytes, little-endian 16-bit
- **Fire-and-forget** — no ACK, no retry; USB CDC serial reliability is sufficient
- **Host traffic takes priority.** Collisions with outgoing fader data are rare due to event-driven firmware, and UART TX/RX are hardware-independent

**Defined commands** (CMD_IDs to be assigned at implementation time):

| Name | Payload | Description |
|---|---|---|
| `CMD_QUERY` | none | Host → firmware: request ready beacon |
| `SET_CHANNEL_NAME` | `[channel_idx][name\0]` | Push display name for channel N |
| `SET_CHANNEL_ICON` | `[channel_idx][bitmap bytes]` | Push icon bitmap for channel N |

### 2. Connection Handshake and Icon Push Trigger

The host must push icons and names to the firmware on every connection. Two scenarios must both be handled:

**Device connects while host is already running:**
- Firmware sends `SERENITY\r\n` after its 1500ms startup delay
- Host detects this line (does not match fader pattern), triggers icon push

**Host launches with device already connected:**
- Host sends `CMD_QUERY` immediately on successfully opening the serial port
- Firmware responds with `SERENITY\r\n`
- Host receives beacon and triggers icon push

`SERENITY\r\n` is the shared ready signal for both paths. The host pushes all 5 channel names then all 5 channel icons on receipt, regardless of how the beacon was produced.

**Manual trigger:** a tray menu item ("Push display icons") fires the same push sequence on demand, without requiring a reconnect.

**Host-side change detection:** the host tracks the last successfully sent name and icon per channel. On a manual push, unchanged channels are skipped. On a connection event, all channels are always pushed (device state after reset is unknown).

### 3. Icon and Name Streaming

Channel displays (OLEDs 1–5) show a text name in the yellow band (top 16px) and a bitmap icon in the blue area (bottom 48px). Both are sourced from the host and pushed over the bidirectional serial protocol. The master OLED is entirely firmware-controlled and receives nothing from the host.

**Naming commands (`SET_CHANNEL_NAME`):**
- Payload: 1-byte channel index (0–4) + null-terminated ASCII string
- `MaxChannelNameLength = 15` — named constant in host code; value will be revisited when firmware font size is finalized
- Firmware stores the name string in RAM for redraws

**Icon commands (`SET_CHANNEL_ICON`):**
- Payload: 1-byte channel index (0–4) + raw 1-bit bitmap bytes
- Firmware pipes bytes directly to the target OLED via I2C without buffering the full bitmap in MCU RAM
- LEN field in the command header specifies exact byte count; icon size is therefore flexible in the protocol

**Icon library:**
- Icons are keyed by channel index, not by app name — the icon for channel N is displayed on OLED N regardless of what app is mapped to that slider
- Source files stored at the path specified by `icon_path` in `config.yaml`
- Naming convention: TBD (depends on final file format decision)
- File format: TBD (PNG likely, but format will be chosen based on what produces the best quality for dot-matrix displays)
- Icon dimensions: TBD — must fit within 128×48px blue area; exact size TBD
- Conversion to 1-bit: both threshold and Floyd-Steinberg dithering supported, selected via `icon_conversion` in `config.yaml`. Threshold is better for clean line art; dithering is better for grayscale sources. No meaningful difference in storage, transmit time, or CPU cost at these image sizes.
- **Bitmap bit order: TBD** — must match the firmware's direct SSD1306 write implementation. Do not assume MSB-first or LSB-first until the firmware's icon write path is implemented and documented in the firmware CLAUDE.md.

**Fallback:** if no icon file is found for a channel, the host sends a generated bitmap of a large X filling the icon area. This also serves as the initial hardware test image.

### 4. System Mic Mute via HID

The SERENITY RGB button sends a custom HID report when pressed. The host intercepts this and toggles the system microphone mute state via OS audio APIs.

**Device identification:**
- USB VID: `0x1209` (pid.codes open-source VID)
- USB PID: `0x0001`
- SERENITY enumerates as a composite USB device: CDC serial + HID

**HID implementation:**
- Cross-platform hidapi library (Go wrapper, e.g. `sstallman/go-hid`) for device enumeration and report reading
- Mic mute toggle abstracted behind a Go interface with `_windows.go` and `_linux.go` implementations, following the same pattern as `session_finder_windows.go` / `session_finder_linux.go`
- Windows: WASAPI/MMDeviceAPI (COM) to toggle default recording device mute
- Linux: PulseAudio/PipeWire via `pactl` or Go bindings

**Custom HID report format: TBD** — co-designed with firmware when the RGB button hardware replacement (common-anode button, currently on order) is installed and the firmware's HID descriptor is finalized. The Consumer Control report (Play/Pause = `0x00CD`) is already implemented in firmware and handled by the OS natively; only the mic mute custom report requires host-side handling.

---

## Codebase Structure

```
pkg/deej/
  cmd/main.go              — entry point
  deej.go                  — main Deej struct, lifecycle
  config.go                — config loading (viper); add new config keys here
  serial.go                — serial I/O, fader parsing, outbound command writer
  session.go               — audio session abstraction
  session_map.go           — slider → session mapping
  session_finder.go        — interface
  session_finder_windows.go
  session_finder_linux.go
  tray.go                  — system tray; add "Push display icons" menu item here
  notify.go                — desktop notifications
  logger.go
  util/util.go
  util/util_windows.go
  util/util_linux.go
```

**New files to be created:**

```
pkg/deej/
  serial_writer.go         — host → firmware command framing and send logic
  display.go               — connection handshake, icon push sequencing, change tracking
  hid.go                   — HID interface definition + device enumeration
  hid_windows.go           — Windows HID + mic mute implementation
  hid_linux.go             — Linux HID + mic mute implementation (best-effort)
  icon/
    icon.go                — icon loading, conversion (threshold + dither), fallback X generation
```

---

## OS Support

| Feature | Windows | Linux |
|---|---|---|
| Fader/serial reading | ✓ (upstream) | ✓ (upstream) |
| Bidirectional serial | ✓ | ✓ |
| Icon/name streaming | ✓ | ✓ |
| HID device reading | ✓ | ✓ (hidraw) |
| Mic mute toggle | ✓ (WASAPI) | best-effort (PulseAudio/PipeWire) |

---

## TBD — Do Not Assume These

| Item | Blocked on |
|---|---|
| Icon file format | User decision (format that produces best dot-matrix quality) |
| Icon file naming convention | Format decision |
| Icon dimensions (px) | Firmware font size / display layout finalization |
| Bitmap bit order for SET_CHANNEL_ICON | Firmware direct SSD1306 write implementation |
| Custom HID report format (mic mute) | RGB button hardware replacement + firmware HID descriptor |
| CMD_ID byte values | Implementation (assign at development time) |
| Linux mic mute implementation details | Development |

---

## Reference

- Firmware repo and hardware ground truth: `C:\Users\Steven\Documents\Solid Models\deej\SERENITY Firmware\SERENITY-Firmware\CLAUDE.md`
- Upstream deej: https://github.com/omriharel/deej
