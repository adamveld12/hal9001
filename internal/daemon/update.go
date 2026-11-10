package daemon

import (
	"context"
	"log/slog"
	"os"

	"hal9001/internal/store"
	"hal9001/internal/update"
)

// newUpdate builds the app self-update service. It is enabled only when this daemon runs from the release app bundle
// with a CI build number and a signing key embedded (update.Detect); anywhere else it reports why it is off.
// HAL9001_UPDATE_CURRENT and HAL9001_UPDATE_FEED_URL replace the detected build and the feed for tests.
func (d *daemon) newUpdate(st *store.Store, log *slog.Logger) *update.Service {
	exe, _ := os.Executable()
	cur, reason := update.Detect(update.DetectOptions{Exe: exe, Env: os.Getenv(update.EnvCurrent)})
	if reason != "" {
		log.Info("app updates are off", "reason", reason)
	} else {
		log.Info("app updates are on", "version", cur.Version, "build", cur.Build)
	}
	return update.New(update.Options{
		Current: cur, Disabled: reason, FeedBase: os.Getenv(update.EnvFeedURL), MacOS: update.MacOSVersion(), Log: log,
		Settings: func() update.Settings {
			cfg := d.effective(d.workspace()).Config.Value
			return update.Settings{Channel: cfg.UpdateChannel, AutoInstall: cfg.UpdateAutoInstall}
		},
		Busy: func(ctx context.Context) (int, error) {
			c, err := st.Counts(ctx)
			return c.FlowRunsRunning, err
		},
	})
}
