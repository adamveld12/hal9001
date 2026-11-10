package notify_test

import (
	"context"
	"testing"

	"hal9001/internal/notify"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

type memLog struct{ rows []store.Notification }

func (m *memLog) CreateNotification(_ context.Context, n *store.Notification) error {
	m.rows = append(m.rows, *n)
	return nil
}

func TestRouterChannelByModeAndSinks(t *testing.T) {
	for _, tc := range []struct {
		via        string
		sinks      int
		wantChan   string
		wantStatus string
		wantTN     bool
	}{
		{"", 0, "terminal-notifier", "sent", true},
		{spec.NotifyViaAuto, 0, "terminal-notifier", "sent", true},
		{spec.NotifyViaAuto, 1, "app", "sent", false},
		{spec.NotifyViaApp, 0, "app", "no_sink", false},
		{spec.NotifyViaApp, 2, "app", "sent", false},
		{spec.NotifyViaTerminal, 1, "terminal-notifier", "sent", true},
		{spec.NotifyViaNone, 1, "none", "logged", false},
	} {
		f := &fakeSystem{have: map[string]bool{"terminal-notifier": true}}
		var sinks notify.Sinks
		for range tc.sinks {
			sinks.Connect()
		}
		log := &memLog{}
		r := notify.NewRouter(notify.RouterOptions{Log: log, Sinks: &sinks, Via: func() string { return tc.via }, Terminal: f.notifier()})
		if err := r.Notify(context.Background(), inbox); err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if got := len(f.calls) > 0; got != tc.wantTN {
			t.Errorf("via %q sinks %d: terminal-notifier run = %v, want %v", tc.via, tc.sinks, got, tc.wantTN)
		}
		if len(log.rows) != 1 || log.rows[0].Channel != tc.wantChan || log.rows[0].Status != tc.wantStatus {
			t.Errorf("via %q sinks %d: log %+v, want %s/%s", tc.via, tc.sinks, log.rows, tc.wantChan, tc.wantStatus)
		}
	}
}

func TestSinkReleaseIsIdempotent(t *testing.T) {
	var s notify.Sinks
	a, b := s.Connect(), s.Connect()
	a()
	a()
	if s.Count() != 1 {
		t.Errorf("count = %d, want 1", s.Count())
	}
	b()
	if s.Count() != 0 {
		t.Errorf("count = %d, want 0", s.Count())
	}
}
