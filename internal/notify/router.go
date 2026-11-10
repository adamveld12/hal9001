package notify

import (
	"context"
	"log/slog"
	"sync/atomic"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Sinks counts the macOS app instances connected to the event stream as notification sinks
// (`GET /api/stream?types=notification.created` with the header X-Hal9001-Notifier: app).
type Sinks struct{ n atomic.Int64 }

// Connect registers a sink; call the returned function when it disconnects.
func (s *Sinks) Connect() (release func()) {
	s.n.Add(1)
	var done atomic.Bool
	return func() {
		if done.CompareAndSwap(false, true) {
			s.n.Add(-1)
		}
	}
}

// Count is the number of connected sinks.
func (s *Sinks) Count() int { return int(s.n.Load()) }

// Log records notifications; implemented by *store.Store. Creating a row announces it as a
// `notification.created` stream event.
type Log interface {
	CreateNotification(ctx context.Context, n *store.Notification) error
}

// RouterOptions configure NewRouter.
type RouterOptions struct {
	Log   Log
	Sinks *Sinks
	// Via returns the current notify_via setting (spec.NotifyVia*); empty means auto.
	Via func() string
	// Terminal delivers through terminal-notifier (see New).
	Terminal core.Notifier
	Logger   *slog.Logger
}

type router struct{ RouterOptions }

// NewRouter returns a core.Notifier that picks the channel by notify_via, delivers, and records every
// notification in the log (which publishes the stream event the app sinks receive):
//
//	auto:              app when at least one sink is connected, else terminal-notifier
//	app:               never terminal-notifier; status no_sink when nobody is connected
//	terminal-notifier: always terminal-notifier
//	none:              log only
func NewRouter(o RouterOptions) core.Notifier {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &router{o}
}

// Channel reports the channel notify_via selects given the number of connected sinks.
func Channel(via string, sinks int) string {
	switch via {
	case spec.NotifyViaNone:
		return store.ChannelNone
	case spec.NotifyViaApp:
		return store.ChannelApp
	case spec.NotifyViaTerminal:
		return store.ChannelTerminalNotifier
	default:
		if sinks > 0 {
			return store.ChannelApp
		}
		return store.ChannelTerminalNotifier
	}
}

func (r *router) Notify(ctx context.Context, n core.Notification) error {
	via := ""
	if r.Via != nil {
		via = r.Via()
	}
	sinks := 0
	if r.Sinks != nil {
		sinks = r.Sinks.Count()
	}
	n.Channel = Channel(via, sinks)
	var derr error
	switch n.Channel {
	case store.ChannelNone:
		n.Status = store.NotifyLogged
	case store.ChannelApp:
		n.Status = store.NotifySent
		if sinks == 0 {
			n.Status = store.NotifyNoSink
		}
	default:
		if derr = r.Terminal.Notify(ctx, n); derr != nil {
			n.Status, n.Error = store.NotifyFailed, derr.Error()
		} else {
			n.Status = store.NotifySent
		}
	}
	if err := r.Log.CreateNotification(ctx, &n); err != nil {
		r.Logger.Warn("notify: record notification", "err", err)
		if derr == nil {
			return err
		}
	}
	return derr
}
