package deej

import "go.uber.org/zap"

// Notifier provides generic notification sending
type Notifier interface {
	Notify(title string, message string)
}

type silentNotifier struct {
	logger *zap.SugaredLogger
}

func newSilentNotifier(logger *zap.SugaredLogger) *silentNotifier {
	return &silentNotifier{logger: logger.Named("notifier")}
}

func (n *silentNotifier) Notify(title string, message string) {
	n.logger.Infow("Notification suppressed", "title", title, "message", message)
}
