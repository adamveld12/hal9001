package e2e

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"hal9001/content"
	"hal9001/internal/imaptest"
)

func imapMsg(uid int, subject string) imaptest.Message {
	return imaptest.Message{
		UID: uid, GmMsgID: uint64(9000 + uid), GmThrID: uint64(8000 + uid), Labels: []string{`"\\Inbox"`},
		Raw: "From: Alice <alice@example.com>\nTo: Me <me@example.com>\nSubject: " + subject + "\nDate: Wed, 04 Nov 2026 09:30:00 +0000\nMessage-ID: <" + subject + "@example.com>\n\nHello " + subject + "\n",
	}
}

const imapUser = "me@example.com"

// startIMAPDaemon starts a daemon with the shipped imap-mail sensor configured
// only through its per-sensor env file, against a fake Gmail-style IMAP server
// holding two messages. IMAP_USER and IMAP_PASSWORD are both secret.
func startIMAPDaemon(t *testing.T, password string) (*Daemon, *imaptest.Server) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	srv := imaptest.Start(t, imaptest.Options{
		Gmail: true, UIDValidity: 5, User: imapUser, Password: password,
		Messages: []imaptest.Message{imapMsg(1, "first"), imapMsg(2, "second")},
	})
	d := newDaemon(t)
	raw, err := content.FS.ReadFile("sensors/imap-mail.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d.Sensor("imap-mail", string(raw))
	d.Write("env.d/sensors/imap-mail.env", strings.Join([]string{
		"IMAP_USER=" + imapUser, "IMAP_PASSWORD=" + password,
		"IMAP_HOST=" + srv.Host, "IMAP_PORT=" + strconv.Itoa(srv.Port), "IMAP_TLS=none", "",
	}, "\n"))
	d.Flow("watch", "id: watch\nenabled: true\nsubscribe: [imap-mail]\nnodes:\n  - {id: note, type: inbox, title: \"{{.input.subject}}\"}\n")
	d.Start()
	return d, srv
}

// The shipped imap-mail starter sensor, configured only through its per-sensor
// env, polls a fake IMAP server: the first poll baselines, a message arriving
// later becomes exactly one event keyed by its X-GM-MSGID, re-polls dedup, and
// the mailbox is only ever examined and peeked.
func TestIMAPMailStarterSensorPollsAndDedups(t *testing.T) {
	const password = "app-password-for-e2e-1234"
	d, srv := startIMAPDaemon(t, password)

	first := d.firstSensorRun("imap-mail")
	if first.Status != "succeeded" || !first.Baseline || first.ItemCount == nil || *first.ItemCount != 2 || *first.NewCount != 0 {
		t.Fatalf("baseline poll: %+v\nstderr: %s", first, first.Stderr.Text)
	}
	if n := len(d.Events("imap-mail")); n != 0 {
		t.Fatalf("baseline poll created %d events", n)
	}

	srv.SetMessages([]imaptest.Message{imapMsg(1, "first"), imapMsg(2, "second"), imapMsg(3, "third")})
	next := d.Poll("imap-mail")
	if next.Status != "succeeded" || next.Baseline || *next.ItemCount != 3 || *next.NewCount != 1 {
		t.Fatalf("poll after a new message: %+v\nstderr: %s", next, next.Stderr.Text)
	}
	evs := d.Events("imap-mail")
	if len(evs) != 1 || evs[0].DedupKey != "9003" || !strings.Contains(string(evs[0].Payload), `"subject":"third"`) {
		t.Fatalf("events: %+v", evs)
	}

	same := d.Poll("imap-mail")
	if *same.ItemCount != 3 || *same.NewCount != 0 || len(d.Events("imap-mail")) != 1 {
		t.Errorf("repeat poll: %+v", same)
	}

	var searches int
	for _, c := range srv.Commands() {
		switch {
		case strings.HasPrefix(c, "UID SEARCH"):
			searches++
			if searches > 1 && !strings.Contains(c, " SINCE ") {
				t.Errorf("poll %d searched without SINCE: %q", searches, c)
			}
		case strings.HasPrefix(c, "SELECT"), strings.HasPrefix(c, "STORE"):
			t.Errorf("the sensor changed the mailbox: %q", c)
		}
	}
	for _, body := range []string{first.Stdout.Text, first.Stderr.Text, next.Stdout.Text, next.Stderr.Text} {
		if strings.Contains(body, password) {
			t.Errorf("the password is stored in sensor run output: %s", body)
		}
	}
}

type envListing struct {
	Items []struct {
		Key             string  `json:"key"`
		Secret          bool    `json:"secret"`
		Value           *string `json:"value"`
		OverridesGlobal bool    `json:"overrides_global"`
	} `json:"items"`
}

func (l envListing) find(t *testing.T, key string) (secret bool, value *string) {
	t.Helper()
	for _, it := range l.Items {
		if it.Key == key {
			return it.Secret, it.Value
		}
	}
	t.Fatalf("key %s not listed: %+v", key, l.Items)
	return
}

// A value marked plain passes through unredacted (events, sensor runs) while the
// secret IMAP_PASSWORD stays redacted everywhere; flipping the key back to secret
// redacts it again on the next poll; listings never return a secret value.
func TestEnvPlainValuesAreNotRedacted(t *testing.T) {
	const password = "app-password-for-e2e-1234"
	d, srv := startIMAPDaemon(t, password)
	d.firstSensorRun("imap-mail")

	// The mail quotes the app password, so its snippet would leak it without redaction.
	mail := func(uid int, subject string) imaptest.Message {
		m := imapMsg(uid, subject)
		m.Raw = strings.Replace(m.Raw, "\n\nHello", "\n\nyour password is "+password+" for", 1)
		return m
	}
	msgs := []imaptest.Message{imapMsg(1, "first"), imapMsg(2, "second")}
	pollNew := func(uid int, subject string) (SensorRun, string) {
		t.Helper()
		msgs = append(msgs, mail(uid, subject))
		srv.SetMessages(msgs)
		run := d.Poll("imap-mail")
		if run.Status != "succeeded" || *run.NewCount != 1 {
			t.Fatalf("poll: %+v\nstderr: %s", run, run.Stderr.Text)
		}
		evs := d.Events("imap-mail")
		return run, string(evs[0].Payload)
	}
	noPassword := func(what string, bodies ...string) {
		t.Helper()
		for _, b := range bodies {
			if strings.Contains(b, password) {
				t.Errorf("%s contains the IMAP_PASSWORD value:\n%s", what, b)
			}
		}
	}

	// Secret by default: the address is redacted.
	run, payload := pollNew(3, "third")
	if strings.Contains(payload, imapUser) || !strings.Contains(payload, "«IMAP_USER»") || !strings.Contains(payload, "«IMAP_PASSWORD»") {
		t.Fatalf("secret IMAP_USER: payload %s", payload)
	}
	noPassword("secret poll", payload, run.Stdout.Text, run.Stderr.Text)

	// Mark IMAP_USER plain without retyping the value.
	d.Put("/api/sensors/imap-mail/env/IMAP_USER", map[string]any{"secret": false}).Want(204)
	if file := d.Read("env.d/sensors/imap-mail.env"); !strings.Contains(file, "# hal:plain IMAP_USER") || !strings.Contains(file, "IMAP_USER="+imapUser) {
		t.Errorf("env file after marking plain:\n%s", file)
	}
	run, payload = pollNew(4, "fourth")
	if !strings.Contains(payload, imapUser) || strings.Contains(payload, "«IMAP_USER»") || !strings.Contains(payload, "«IMAP_PASSWORD»") {
		t.Fatalf("plain IMAP_USER: payload %s", payload)
	}
	noPassword("plain poll", payload, run.Stdout.Text, run.Stderr.Text)
	if !strings.Contains(run.Stdout.Text, imapUser) {
		t.Errorf("plain value is redacted in the sensor run stdout: %s", run.Stdout.Text)
	}

	// Listings: the plain value is readable, the secrets are not.
	resp := d.Get("/api/sensors/imap-mail/env").Want(200)
	var l envListing
	resp.JSON(&l)
	if secret, v := l.find(t, "IMAP_USER"); secret || v == nil || *v != imapUser {
		t.Errorf("IMAP_USER listed as secret=%v value=%v", secret, v)
	}
	if secret, v := l.find(t, "IMAP_PASSWORD"); !secret || v != nil {
		t.Errorf("IMAP_PASSWORD listed as secret=%v value=%v", secret, v)
	}
	noPassword("sensor env listing", string(resp.Body))

	// Changing a plain key's value keeps it plain only when secret:false is sent again.
	d.Put("/api/sensors/imap-mail/env/IMAP_USER", map[string]any{"value": imapUser, "secret": false}).Want(204)
	d.Get("/api/sensors/imap-mail/env").Want(200).JSON(&l)
	if secret, _ := l.find(t, "IMAP_USER"); secret {
		t.Error("IMAP_USER lost its plain flag when replaced with secret:false")
	}

	// Back to secret: redacted again on the next poll, and the marker is gone from the file.
	d.Put("/api/sensors/imap-mail/env/IMAP_USER", map[string]any{"secret": true}).Want(204)
	if file := d.Read("env.d/sensors/imap-mail.env"); strings.Contains(file, "hal:plain") {
		t.Errorf("marker left in the env file:\n%s", file)
	}
	_, payload = pollNew(5, "fifth")
	if strings.Contains(payload, imapUser) || !strings.Contains(payload, "«IMAP_USER»") {
		t.Fatalf("IMAP_USER flipped back to secret: payload %s", payload)
	}
	resp = d.Get("/api/sensors/imap-mail/env").Want(200)
	resp.JSON(&l)
	if secret, v := l.find(t, "IMAP_USER"); !secret || v != nil || strings.Contains(string(resp.Body), imapUser) {
		t.Errorf("IMAP_USER after flipping back: secret=%v value=%v body=%s", secret, v, resp.Body)
	}

	// Global env: plain flag round-trips, a flag change on a missing key is a 404, an empty body a 422.
	d.Put("/api/env/SHARED_NAME", map[string]any{"value": "visible-value-1", "secret": false}).Want(204)
	d.Put("/api/env/SHARED_TOKEN", map[string]any{"value": "hidden-value-2"}).Want(204)
	d.Put("/api/env/NOPE", map[string]any{"secret": false}).Want(404)
	d.Put("/api/env/SHARED_NAME", map[string]any{}).Want(422)
	resp = d.Get("/api/env").Want(200)
	var g envListing
	resp.JSON(&g)
	if secret, v := g.find(t, "SHARED_NAME"); secret || v == nil || *v != "visible-value-1" {
		t.Errorf("SHARED_NAME listed as secret=%v value=%v", secret, v)
	}
	if secret, v := g.find(t, "SHARED_TOKEN"); !secret || v != nil || strings.Contains(string(resp.Body), "hidden-value-2") {
		t.Errorf("SHARED_TOKEN listed as secret=%v value=%v", secret, v)
	}
	d.Delete("/api/env/SHARED_NAME").Want(204)
	if file := d.Read("env"); strings.Contains(file, "hal:plain") {
		t.Errorf("deleting a plain key left its marker:\n%s", file)
	}
}
