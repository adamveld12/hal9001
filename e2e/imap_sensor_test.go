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

// The shipped imap-mail starter sensor, configured only through its per-sensor
// env, polls a fake IMAP server: the first poll baselines, a message arriving
// later becomes exactly one event keyed by its X-GM-MSGID, re-polls dedup, and
// the mailbox is only ever examined and peeked.
func TestIMAPMailStarterSensorPollsAndDedups(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	const password = "app-password-for-e2e-1234"
	srv := imaptest.Start(t, imaptest.Options{
		Gmail: true, UIDValidity: 5, User: "me@example.com", Password: password,
		Messages: []imaptest.Message{imapMsg(1, "first"), imapMsg(2, "second")},
	})

	d := newDaemon(t)
	raw, err := content.FS.ReadFile("sensors/imap-mail.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d.Sensor("imap-mail", string(raw))
	d.Write("env.d/sensors/imap-mail.env", strings.Join([]string{
		"IMAP_USER=me@example.com", "IMAP_PASSWORD=" + password,
		"IMAP_HOST=" + srv.Host, "IMAP_PORT=" + strconv.Itoa(srv.Port), "IMAP_TLS=none", "",
	}, "\n"))
	d.Flow("watch", "id: watch\nenabled: true\nsubscribe: [imap-mail]\nnodes:\n  - {id: note, type: inbox, title: \"{{.input.subject}}\"}\n")
	d.Start()

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
