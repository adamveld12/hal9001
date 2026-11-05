package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type sensorStatusJSON struct {
	Status struct {
		State      string  `json:"state"`
		Paused     bool    `json:"paused"`
		PausedAt   *string `json:"paused_at"`
		PausedBy   *string `json:"paused_by"`
		NextPollAt *string `json:"next_poll_at"`
	} `json:"status"`
}

func (d *Daemon) sensorStatus(id string) sensorStatusJSON {
	d.t.Helper()
	var s sensorStatusJSON
	d.Get("/api/sensors/" + id).Want(200).JSON(&s)
	return s
}

// settledRuns returns the runs of sensor tick once none is running.
func (d *Daemon) settledRuns() []SensorRun {
	d.t.Helper()
	var l sensorRunList
	d.Eventually("no running poll of tick", func() bool {
		d.Get("/api/sensors/tick/runs?limit=200").Want(200).JSON(&l)
		for _, r := range l.Items {
			if r.Status == "running" {
				return false
			}
		}
		return true
	})
	return l.Items
}

func countTrigger(runs []SensorRun, trigger string) int {
	n := 0
	for _, r := range runs {
		if r.Trigger == trigger {
			n++
		}
	}
	return n
}

// A paused sensor is never polled by the scheduler, survives a daemon restart
// as paused, and still answers Poll now and event injection. Resume schedules
// polls again.
func TestSensorPauseStopsScheduledPolls(t *testing.T) {
	d := newDaemon(t)
	d.Sensor("tick", "id: tick\ninterval: 1s\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	d.Flow("watch", "id: watch\nenabled: true\nsubscribe: [tick]\nnodes:\n  - {id: note, type: inbox, title: \"item {{.input.id}}\"}\n")
	d.Start()

	d.Eventually("two scheduled polls", func() bool {
		var l sensorRunList
		d.Get("/api/sensors/tick/runs?limit=200").Want(200).JSON(&l)
		return countTrigger(l.Items, "scheduled") >= 2
	})
	if s := d.sensorStatus("tick"); s.Status.Paused || s.Status.PausedAt != nil || s.Status.NextPollAt == nil {
		t.Fatalf("before pause: %+v", s.Status)
	}

	var paused sensorStatusJSON
	d.Post("/api/sensors/tick/pause", nil).Want(200).JSON(&paused)
	st := paused.Status
	if !st.Paused || st.State != "paused" || st.NextPollAt != nil || st.PausedAt == nil || st.PausedBy == nil {
		t.Fatalf("pause response status: %+v", st)
	}
	// Idempotent: a second pause keeps who and when.
	var again sensorStatusJSON
	d.Post("/api/sensors/tick/pause", nil).Want(200).JSON(&again)
	if again.Status.PausedAt == nil || *again.Status.PausedAt != *st.PausedAt {
		t.Errorf("second pause changed paused_at: %v -> %v", *st.PausedAt, again.Status.PausedAt)
	}
	var list struct {
		Items []sensorStatusJSON `json:"items"`
	}
	d.Get("/api/sensors").Want(200).JSON(&list)
	if len(list.Items) == 0 {
		t.Fatal("no sensors listed")
	}
	for _, it := range list.Items {
		if it.Status.Paused != (it.Status.PausedAt != nil) {
			t.Errorf("list status is inconsistent: %+v", it.Status)
		}
	}
	d.Post("/api/sensors/nope/pause", nil).Want(404)
	d.Post("/api/sensors/nope/resume", nil).Want(404)

	before := len(d.settledRuns())
	time.Sleep(3500 * time.Millisecond) // three intervals
	if after := len(d.settledRuns()); after != before {
		t.Fatalf("a paused sensor was polled: %d runs before, %d after 3.5s", before, after)
	}

	// Explicit actions still work while paused.
	if run := d.Poll("tick"); run.Trigger != "manual" || run.Status != "succeeded" {
		t.Errorf("Poll now while paused: %+v", run)
	}
	d.Post("/api/events?sensor_id=tick", map[string]any{"id": "x"}).Want(201)
	paused2 := len(d.settledRuns())

	// The pause survives a restart: no scheduled poll follows it.
	d.Restart()
	if s := d.sensorStatus("tick"); !s.Status.Paused || s.Status.State != "paused" || s.Status.NextPollAt != nil {
		t.Fatalf("after restart: %+v", s.Status)
	}
	time.Sleep(2500 * time.Millisecond)
	if n := len(d.settledRuns()); n != paused2 {
		t.Fatalf("a paused sensor polled after a restart: %d runs, want %d", n, paused2)
	}

	var resumed sensorStatusJSON
	d.Post("/api/sensors/tick/resume", nil).Want(200).JSON(&resumed)
	if resumed.Status.Paused || resumed.Status.PausedAt != nil || resumed.Status.PausedBy != nil || resumed.Status.State == "paused" {
		t.Fatalf("resume response status: %+v", resumed.Status)
	}
	d.Post("/api/sensors/tick/resume", nil).Want(200) // idempotent
	d.Eventually("a scheduled poll after resume", func() bool {
		var l sensorRunList
		d.Get("/api/sensors/tick/runs?limit=200").Want(200).JSON(&l)
		return len(l.Items) > paused2
	})
	if s := d.sensorStatus("tick"); s.Status.NextPollAt == nil {
		t.Errorf("resumed sensor has no next_poll_at: %+v", s.Status)
	}
}

func TestSensorPauseCLI(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Start()
	if _, errOut, code := d.CLI("", "sensors", "pause", "src"); code != 0 {
		t.Fatalf("sensors pause: exit %d: %s", code, errOut)
	}
	if !d.sensorStatus("src").Status.Paused {
		t.Error("CLI pause did not pause the sensor")
	}
	if _, errOut, code := d.CLI("", "sensors", "resume", "src"); code != 0 {
		t.Fatalf("sensors resume: exit %d: %s", code, errOut)
	}
	if d.sensorStatus("src").Status.Paused {
		t.Error("CLI resume did not resume the sensor")
	}
}

func reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// envItem is what the env sensor prints: the value as the process saw it
// (redacted on the way in), the same value reversed (not redacted, so the test
// can see what the process got) and the one only the global env has.
type envItem struct {
	Tok  string `json:"tok"`
	Rev  string `json:"rev"`
	Glob string `json:"glob"`
	Own  string `json:"own"`
}

func TestSensorEnvOverridesGlobalAndIsRedacted(t *testing.T) {
	const (
		globalVal = "global-secret-value-1111"
		sensorVal = "sensor-secret-value-2222"
		ownVal    = "only-on-this-sensor-3333"
		onlyGlob  = "only-global-secret-4444"
	)
	d := newDaemon(t)
	d.Env("SENSOR_TOKEN=" + globalVal + "\nONLY_GLOBAL=" + onlyGlob + "\n")
	d.Sensor("tick", `id: tick
interval: 1h
lang: bash
script: |
  printf '{"id":"%s","tok":"%s","rev":"%s","glob":"%s","own":"%s"}\n' "$$" "$SENSOR_TOKEN" "$(printf %s "$SENSOR_TOKEN" | rev)" "$ONLY_GLOBAL" "${OWN_KEY:-unset}"
  echo "token is $SENSOR_TOKEN and ${OWN_KEY:-unset}" >&2
key: "{{.id}}"
first_run: fire
timeout: 10s
`)
	d.Start()

	poll := func() (SensorRun, envItem) {
		t.Helper()
		run := d.Poll("tick")
		if run.Status != "succeeded" {
			t.Fatalf("poll: %+v", run)
		}
		var it envItem
		if err := json.Unmarshal([]byte(strings.TrimSpace(run.Stdout.Text)), &it); err != nil {
			t.Fatalf("stdout is not the item: %v\n%s", err, run.Stdout.Text)
		}
		return run, it
	}
	noSecrets := func(what, body string, secrets ...string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("%s contains the secret %q:\n%s", what, s, body)
			}
		}
	}

	// Global value only.
	_, it := poll()
	if it.Rev != reverse(globalVal) || it.Tok != "«SENSOR_TOKEN»" || it.Own != "unset" {
		t.Fatalf("global only: %+v", it)
	}

	// Per-sensor values: the sensor's wins; the other key exists only there.
	d.Put("/api/sensors/tick/env/SENSOR_TOKEN", map[string]any{"value": sensorVal}).Want(204)
	d.Put("/api/sensors/tick/env/OWN_KEY", map[string]any{"value": ownVal}).Want(204)

	var keys struct {
		Items []struct {
			Key             string `json:"key"`
			OverridesGlobal bool   `json:"overrides_global"`
		} `json:"items"`
	}
	resp := d.Get("/api/sensors/tick/env").Want(200)
	resp.JSON(&keys)
	noSecrets("GET sensor env", string(resp.Body), sensorVal, ownVal, globalVal)
	if len(keys.Items) != 2 || keys.Items[0].Key != "OWN_KEY" || keys.Items[0].OverridesGlobal ||
		keys.Items[1].Key != "SENSOR_TOKEN" || !keys.Items[1].OverridesGlobal {
		t.Fatalf("sensor env keys: %+v", keys.Items)
	}
	// The global list does not show the sensor's keys.
	if body := string(d.Get("/api/env").Want(200).Body); strings.Contains(body, "OWN_KEY") {
		t.Errorf("global env lists a per-sensor key: %s", body)
	}

	// The file is private and outside the sensor YAML.
	info, err := os.Stat(d.Path("env.d/sensors/tick.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("env file mode %v, want 0600", info.Mode().Perm())
	}
	if dir, err := os.Stat(d.Path("env.d/sensors")); err != nil || dir.Mode().Perm() != 0o700 {
		t.Errorf("env.d/sensors mode: %v %v", dir, err)
	}
	if yaml := d.Read("sensors/tick.yaml"); strings.Contains(yaml, sensorVal) || strings.Contains(yaml, ownVal) {
		t.Errorf("per-sensor env leaked into the sensor YAML:\n%s", yaml)
	}
	if gi := d.Read(".gitignore"); !strings.Contains(gi, "env.d/") {
		t.Errorf(".gitignore does not ignore env.d/:\n%s", gi)
	}

	// Effective on the next poll, no restart.
	run, it := poll()
	if it.Rev != reverse(sensorVal) || it.Own != "«OWN_KEY»" {
		t.Fatalf("with sensor env: %+v", it)
	}
	if it.Tok != "«SENSOR_TOKEN»" {
		t.Errorf("tok = %q, want the redaction marker", it.Tok)
	}
	if it.Glob != "«ONLY_GLOBAL»" {
		t.Errorf("the global-only key is gone for the sensor: %+v", it)
	}
	for what, body := range map[string]string{
		"sensor run stdout": run.Stdout.Text, "sensor run stderr": run.Stderr.Text, "sensor run": run.Error,
	} {
		noSecrets(what, body, sensorVal, ownVal, globalVal, onlyGlob)
	}
	if !strings.Contains(run.Stderr.Text, "«SENSOR_TOKEN»") || !strings.Contains(run.Stderr.Text, "«OWN_KEY»") {
		t.Errorf("stderr should show the redaction markers: %q", run.Stderr.Text)
	}
	// ... nor in anything derived from the run.
	for _, path := range []string{"/api/sensors/tick", "/api/sensors", "/api/sensors/tick/runs?limit=200", "/api/sensor-runs/" + run.ID, "/api/events?limit=200", "/api/settings", "/api/env"} {
		noSecrets("GET "+path, string(d.Get(path).Want(200).Body), sensorVal, ownVal)
	}
	for _, ev := range d.Events("tick") {
		noSecrets("event payload", string(ev.Payload), sensorVal, ownVal, globalVal)
	}

	// Unset restores the global value.
	d.Delete("/api/sensors/tick/env/SENSOR_TOKEN").Want(204)
	d.Delete("/api/sensors/tick/env/SENSOR_TOKEN").Want(404)
	if _, it = poll(); it.Rev != reverse(globalVal) {
		t.Errorf("after unset the sensor sees %+v, want the global value", it)
	}
	d.Delete("/api/sensors/tick/env/OWN_KEY").Want(204)
	if _, it = poll(); it.Own != "unset" {
		t.Errorf("after unset OWN_KEY is still visible: %+v", it)
	}
	resp = d.Get("/api/sensors/tick/env").Want(200)
	resp.JSON(&keys)
	if len(keys.Items) != 0 {
		t.Errorf("keys after unset: %+v", keys.Items)
	}

	// Unknown sensor, bad key.
	d.Get("/api/sensors/nope/env").Want(404)
	d.Put("/api/sensors/nope/env/K", map[string]any{"value": "v"}).Want(404)
	d.Put("/api/sensors/tick/env/1BAD", map[string]any{"value": "v"}).Want(400, 422)
}

func TestSensorEnvDeletedWithSensorAndHotReloaded(t *testing.T) {
	d := newDaemon(t)
	d.Sensor("tick", "id: tick\ninterval: 1h\nlang: bash\nscript: |\n  printf '{\"id\":\"1\",\"v\":\"%s\"}\\n' \"${MY_VAR:-none}\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	d.Start()
	d.Put("/api/sensors/tick/env/MY_VAR", map[string]any{"value": "a-long-enough-secret"}).Want(204)
	if out := d.Poll("tick").Stdout.Text; !strings.Contains(out, "«MY_VAR»") {
		t.Fatalf("the sensor did not get MY_VAR: %s", out)
	}

	// An edit on disk (not through the API) is picked up by the watcher.
	d.Write("env.d/sensors/tick.env", "")
	d.Eventually("the on-disk edit to apply", func() bool {
		return strings.Contains(d.Poll("tick").Stdout.Text, "none")
	})

	d.Put("/api/sensors/tick/env/MY_VAR", map[string]any{"value": "a-long-enough-secret"}).Want(204)
	var det struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/sensors/tick").Want(200).JSON(&det)
	d.Delete("/api/sensors/tick?expected_hash=" + det.Hash).Want(204)
	if _, err := os.Stat(d.Path("env.d/sensors/tick.env")); !os.IsNotExist(err) {
		t.Errorf("the env file survived the sensor: %v", err)
	}
}

func TestSensorEnvCLI(t *testing.T) {
	d := newDaemon(t)
	d.Env("TOKEN=global-token-value-9999\n")
	d.IdleSensor("src")
	d.Start()
	if _, errOut, code := d.CLI("sensor-token-value-8888\n", "sensors", "env", "set", "src", "TOKEN", "--stdin"); code != 0 {
		t.Fatalf("sensors env set: exit %d: %s", code, errOut)
	}
	out, errOut, code := d.CLI("", "sensors", "env", "list", "src")
	if code != 0 {
		t.Fatalf("sensors env list: exit %d: %s", code, errOut)
	}
	if strings.Contains(out, "sensor-token-value-8888") || !strings.Contains(out, "TOKEN") {
		t.Errorf("sensors env list output: %s", out)
	}
	if _, errOut, code := d.CLI("", "sensors", "env", "unset", "src", "TOKEN"); code != 0 {
		t.Fatalf("sensors env unset: exit %d: %s", code, errOut)
	}
	if _, _, code := d.CLI("", "sensors", "env", "unset", "src", "TOKEN"); code == 0 {
		t.Error("unsetting a missing key succeeded")
	}
}

// Init adds env.d/ to the .gitignore of a home that predates it.
func TestInitAppendsEnvDToExistingGitignore(t *testing.T) {
	d := newDaemon(t)
	if err := os.WriteFile(d.Path(".gitignore"), []byte("data/\nenv\nmine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Start()
	gi := d.Read(".gitignore")
	if !strings.Contains(gi, "env.d/") || !strings.Contains(gi, "mine") || strings.Count(gi, "data/") != 1 {
		t.Errorf(".gitignore = %q", gi)
	}
}
