package deej

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"go.uber.org/zap"
)

// Command IDs for the host → firmware binary protocol.
// Frame format: [0x00][cmdID][lenLo][lenHi][...payload...]
const (
	cmdPrefix          = byte(0x00)
	cmdQuery           = byte(0x01)
	cmdSetChName       = byte(0x02)
	cmdSetChIcon       = byte(0x03)
	cmdSetMasterVol    = byte(0x04)
	cmdSetMicMuteState = byte(0x05)

	// MaxChannelNameLength is the maximum number of characters in a channel display
	// name (excluding the null terminator). Revisit when firmware font size is finalized.
	MaxChannelNameLength = 15
)

// SerialWriter frames and sends host→firmware commands over the open serial connection.
// All public methods are safe for concurrent use.
type SerialWriter struct {
	w      io.Writer
	mu     sync.Mutex
	logger *zap.SugaredLogger
}

// NewSerialWriter creates a SerialWriter that writes framed commands to w.
func NewSerialWriter(w io.Writer, logger *zap.SugaredLogger) *SerialWriter {
	return &SerialWriter{
		w:      w,
		logger: logger.Named("serial_writer"),
	}
}

// SendQuery sends CMD_QUERY, asking SERENITY to emit its ready beacon.
func (sw *SerialWriter) SendQuery() error {
	return sw.send(cmdQuery, nil)
}

// SendChannelName pushes a display name for channel idx (0–4).
// Names longer than MaxChannelNameLength are silently truncated.
func (sw *SerialWriter) SendChannelName(idx byte, name string) error {
	if len(name) > MaxChannelNameLength {
		name = name[:MaxChannelNameLength]
	}
	payload := make([]byte, 0, 1+len(name)+1)
	payload = append(payload, idx)
	payload = append(payload, name...)
	payload = append(payload, 0x00)
	return sw.send(cmdSetChName, payload)
}

// SendChannelIcon pushes a raw 1-bit bitmap for channel idx (0–4).
func (sw *SerialWriter) SendChannelIcon(idx byte, bitmap []byte) error {
	payload := make([]byte, 0, 1+len(bitmap))
	payload = append(payload, idx)
	payload = append(payload, bitmap...)
	return sw.send(cmdSetChIcon, payload)
}

// SendMasterVolume pushes the current master volume, raw 0–1023 (same domain as
// the firmware's own masterVol), so SERENITY can sync its encoder/display state
// instead of booting hard-coded.
func (sw *SerialWriter) SendMasterVolume(raw uint16) error {
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, raw)
	return sw.send(cmdSetMasterVol, payload)
}

// SendMicMuteState pushes the current system microphone mute state so SERENITY's
// RGB button LED can sync to it instead of booting unmuted.
func (sw *SerialWriter) SendMicMuteState(muted bool) error {
	payload := []byte{0x00}
	if muted {
		payload[0] = 0x01
	}
	return sw.send(cmdSetMicMuteState, payload)
}

func (sw *SerialWriter) send(cmdID byte, payload []byte) error {
	frame := make([]byte, 4+len(payload))
	frame[0] = cmdPrefix
	frame[1] = cmdID
	binary.LittleEndian.PutUint16(frame[2:4], uint16(len(payload)))
	copy(frame[4:], payload)

	sw.mu.Lock()
	defer sw.mu.Unlock()

	if _, err := sw.w.Write(frame); err != nil {
		sw.logger.Warnw("Failed to send command", "cmdID", fmt.Sprintf("0x%02x", cmdID), "error", err)
		return fmt.Errorf("send command 0x%02x: %w", cmdID, err)
	}

	sw.logger.Debugw("Sent command", "cmdID", fmt.Sprintf("0x%02x", cmdID), "payloadLen", len(payload))
	return nil
}
