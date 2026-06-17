package deej

import (
	"go.uber.org/zap"
)

// numChannels is the number of channel OLEDs on SERENITY (indices 0–4, mapped to faders 1–5).
// The master OLED is firmware-controlled and receives no host commands.
const numChannels = 5

// DisplayManager handles the SERENITY connection handshake and channel display push sequencing.
type DisplayManager struct {
	deej   *Deej
	logger *zap.SugaredLogger

	// last successfully sent state per channel; used to skip unchanged channels on manual push
	lastSentNames [numChannels]string
	lastSentIcons [numChannels][]byte
}

// NewDisplayManager creates a DisplayManager and wires it to SerialIO connection and beacon events.
func NewDisplayManager(deej *Deej, logger *zap.SugaredLogger) (*DisplayManager, error) {
	logger = logger.Named("display")

	dm := &DisplayManager{
		deej:   deej,
		logger: logger,
	}

	dm.subscribeToSerialEvents()

	logger.Debug("Created display manager")
	return dm, nil
}

// TriggerPush sends all channel names and icons to SERENITY, skipping channels
// whose state hasn't changed since the last successful push.
func (dm *DisplayManager) TriggerPush() {
	writer := dm.deej.serial.Writer()
	if writer == nil {
		dm.logger.Warn("Push requested but serial is not connected")
		return
	}
	dm.pushAll(writer, false)
}

func (dm *DisplayManager) subscribeToSerialEvents() {
	connectCh := dm.deej.serial.SubscribeToConnectEvents()
	beaconCh := dm.deej.serial.SubscribeToBeaconEvents()

	go func() {
		for {
			select {
			case <-connectCh:
				dm.logger.Debug("Serial connected, sending CMD_QUERY")
				writer := dm.deej.serial.Writer()
				if writer == nil {
					dm.logger.Warn("Connect event received but writer is nil")
					continue
				}
				if err := writer.SendQuery(); err != nil {
					dm.logger.Warnw("Failed to send CMD_QUERY", "error", err)
				}

			case <-beaconCh:
				dm.logger.Info("Beacon received, pushing display data")
				writer := dm.deej.serial.Writer()
				if writer == nil {
					dm.logger.Warn("Beacon received but writer is nil")
					continue
				}
				dm.pushAll(writer, true)
			}
		}
	}()
}

// pushAll sends names and icons for all channels. When force is true (connection event),
// all channels are sent regardless of change tracking; when false (manual push),
// unchanged channels are skipped.
func (dm *DisplayManager) pushAll(writer *SerialWriter, force bool) {
	names := dm.deej.config.ChannelNames

	for i := 0; i < numChannels; i++ {
		name := names[i]

		if !force && name == dm.lastSentNames[i] {
			continue
		}

		if err := writer.SendChannelName(byte(i), name); err != nil {
			dm.logger.Warnw("Failed to send channel name", "channel", i, "error", err)
			continue
		}

		dm.lastSentNames[i] = name
		dm.logger.Debugw("Sent channel name", "channel", i, "name", name)
	}

	// TODO: push channel icons once icon loading is implemented
}
