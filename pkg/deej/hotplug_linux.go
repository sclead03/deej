package deej

import (
	"time"

	"go.uber.org/zap"
)

// waitForSerialDevice on Linux has no udev implementation.
// Returns after a brief delay so the caller can retry the connection.
func waitForSerialDevice(logger *zap.SugaredLogger) {
	time.Sleep(2 * time.Second)
}
