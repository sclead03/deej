package deej

import (
	"fmt"
	"io"
	"time"

	"go.uber.org/zap"
)

const (
	hidVendorID  = 0x1209
	hidProductID = 0x0001

	hidReconnectDelay = 2 * time.Second

	// micMuteReportID is the report ID SERENITY's RGB button press sends
	// (firmware's kMicMuteDesc, report ID 4) - distinct from the Consumer
	// Control Play/Pause report (ID 3) the encoder double-click sends, which
	// arrives on this same shared HID interface and must be ignored here.
	micMuteReportID = 0x04

	// Sentinels used in mic_mute.mute_action / mic_mute.unmute_action config lists.
	micMuteSentinelAll   = "mute.all"
	micUnmuteSentinelAll = "unmute.all"
)

// MicMuter applies mute/unmute to configured capture devices and reports current state.
type MicMuter interface {
	MuteDevices(targets []string) error
	UnmuteDevices(targets []string) error
	IsMuted() (bool, error) // reads default capture device; used for connect-time state init
}

// HIDManager reads reports from the SERENITY HID interface and dispatches actions.
type HIDManager struct {
	deej   *Deej
	logger *zap.SugaredLogger
	muter  MicMuter
	stopCh chan struct{}

	// currentMuted is the host's authoritative mic-mute state, updated by button
	// presses and by external OS changes forwarded via SetCurrentMuteState.
	currentMuted      bool
	currentMutedKnown bool
}

// NewHIDManager creates a HIDManager.
func NewHIDManager(deej *Deej, logger *zap.SugaredLogger) (*HIDManager, error) {
	logger = logger.Named("hid")

	muter, err := newMicMuter(logger)
	if err != nil {
		return nil, fmt.Errorf("create mic muter: %w", err)
	}

	return &HIDManager{
		deej:   deej,
		logger: logger,
		muter:  muter,
		stopCh: make(chan struct{}),
	}, nil
}

// Start begins watching for the SERENITY HID device in the background.
func (h *HIDManager) Start() {
	go h.run()
}

// Stop shuts down the HID manager.
func (h *HIDManager) Stop() {
	close(h.stopCh)
}

// IsMicMuted returns the current mic mute state. Uses the tracked state if a
// button press or external change has been observed; otherwise queries the
// default capture device (connect-time init path).
func (h *HIDManager) IsMicMuted() (bool, error) {
	if h.currentMutedKnown {
		return h.currentMuted, nil
	}
	return h.muter.IsMuted()
}

// SetCurrentMuteState records an externally-observed mic mute state change
// (e.g. from the Windows volume mixer) so that the next button press
// correctly toggles from the real current state rather than a stale one.
func (h *HIDManager) SetCurrentMuteState(muted bool) {
	h.currentMuted = muted
	h.currentMutedKnown = true
}

func (h *HIDManager) run() {
	h.logger.Debug("HID manager started")

	for {
		select {
		case <-h.stopCh:
			h.logger.Debug("HID manager stopping")
			return
		default:
		}

		dev, err := openSERENITY()
		if err != nil {
			h.logger.Debugw("SERENITY HID device not found, retrying", "delay", hidReconnectDelay)
			select {
			case <-h.stopCh:
				return
			case <-time.After(hidReconnectDelay):
			}
			continue
		}

		h.logger.Info("SERENITY HID device connected")
		h.readLoop(dev)
		h.logger.Info("SERENITY HID device disconnected")
	}
}

func (h *HIDManager) readLoop(r io.ReadCloser) {
	type result struct {
		data []byte
		err  error
	}

	done := make(chan struct{})
	ch := make(chan result)

	go func() {
		buf := make([]byte, 64)
		for {
			n, err := r.Read(buf)
			if err != nil {
				select {
				case ch <- result{err: err}:
				case <-done:
				}
				return
			}
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case ch <- result{data: data}:
				case <-done:
					return
				}
			}
		}
	}()

	defer close(done)
	defer r.Close()

	for {
		select {
		case <-h.stopCh:
			return
		case res := <-ch:
			if res.err != nil {
				return
			}
			h.handleReport(res.data)
		}
	}
}

func (h *HIDManager) handleReport(report []byte) {
	if len(report) == 0 || report[0] != micMuteReportID {
		h.logger.Debugw("Ignoring HID report not for mic mute", "report", report)
		return
	}

	h.logger.Debug("Received mic-mute HID report")

	// If we haven't observed a button press or external change yet, read the
	// real default-device state so we toggle in the right direction.
	if !h.currentMutedKnown {
		if muted, err := h.muter.IsMuted(); err == nil {
			h.currentMuted = muted
		}
		h.currentMutedKnown = true
	}

	newMuted := !h.currentMuted

	// Mark before applying: SetMute's COM notification can fire synchronously
	// on this call stack before MuteDevices/UnmuteDevices returns, so the
	// suppression window must already be open (see session_map.go).
	h.deej.sessions.markMicMuteSetByButton()

	cfg := h.deej.config.MicMute
	var err error
	if newMuted {
		err = h.muter.MuteDevices(cfg.MuteAction)
	} else {
		err = h.muter.UnmuteDevices(cfg.UnmuteAction)
	}
	if err != nil {
		h.logger.Warnw("Failed to apply mic mute action", "muting", newMuted, "error", err)
		return
	}

	h.currentMuted = newMuted
	h.currentMutedKnown = true

	writer := h.deej.serial.Writer()
	if writer == nil {
		return
	}
	if err := writer.SendMicMuteState(newMuted); err != nil {
		h.logger.Warnw("Failed to push mic mute state after button action", "error", err)
		return
	}

	h.logger.Debugw("Pushed mic mute state after button action", "muted", newMuted)
}
