package deej

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// openSERENITY on Linux is not yet implemented.
// A future implementation would enumerate /dev/hidraw* and match by VID/PID
// via /sys/class/hidraw/<dev>/device/uevent.
func openSERENITY() (io.ReadCloser, error) {
	return nil, errors.New("HID enumeration not yet implemented on Linux")
}

type linuxMicMuter struct {
	logger *zap.SugaredLogger
}

func newMicMuter(logger *zap.SugaredLogger) (MicMuter, error) {
	return &linuxMicMuter{logger: logger.Named("mic_muter")}, nil
}

func (m *linuxMicMuter) MuteDevices(targets []string) error {
	return m.applyPactl(targets, micMuteSentinelAll, "1")
}

func (m *linuxMicMuter) UnmuteDevices(targets []string) error {
	return m.applyPactl(targets, micUnmuteSentinelAll, "0")
}

func (m *linuxMicMuter) applyPactl(targets []string, sentinel, state string) error {
	for _, t := range targets {
		source := t
		if strings.EqualFold(t, sentinel) {
			source = "@DEFAULT_SOURCE@"
		}
		if err := exec.Command("pactl", "set-source-mute", source, state).Run(); err != nil {
			m.logger.Warnw("Failed to set source mute via pactl", "source", source, "state", state, "error", err)
		}
	}
	return nil
}

// IsMuted reports the current system microphone mute state via pactl.
func (m *linuxMicMuter) IsMuted() (bool, error) {
	out, err := exec.Command("pactl", "get-source-mute", "@DEFAULT_SOURCE@").Output()
	if err != nil {
		return false, fmt.Errorf("pactl get mic mute state: %w", err)
	}

	return strings.Contains(string(out), "yes"), nil
}
