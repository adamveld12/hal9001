package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/imaptest"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
)

const (
	imapUser = "me@example.com"
	imapPass = "right-app-password-123"
)

// runIMAP runs the imap-mail sensor script with python3 against srv. extra
// overrides or (with an empty value) removes variables.
func runIMAP(t *testing.T, srv *imaptest.Server, extra map[string]string) result {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "sensor.py")
	if err := os.WriteFile(script, []byte(loadSensor(t, "imap-mail").Script), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"IMAP_USER": imapUser, "IMAP_PASSWORD": imapPass, "IMAP_TLS": "none"}
	if srv != nil {
		env["IMAP_HOST"], env["IMAP_PORT"] = srv.Host, strconv.Itoa(srv.Port)
	}
	for k, v := range extra {
		env[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + filepath.Dir(py) + ":/usr/bin:/bin", "HOME=" + dir}
	for k, v := range env {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exit := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		exit = ee.ExitCode()
	}
	res := result{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	for dec.More() {
		var item map[string]any
		if err := dec.Decode(&item); err != nil {
			t.Fatalf("stdout is not JSONL: %v\nstdout:\n%s\nstderr:\n%s", err, res.stdout, res.stderr)
		}
		res.items = append(res.items, item)
	}
	return res
}

func simpleMsg(uid int, subject string) imaptest.Message {
	return imaptest.Message{
		UID: uid, GmMsgID: uint64(1000 + uid), GmThrID: uint64(500 + uid),
		Raw: "From: A <a@example.com>\nTo: me@example.com\nSubject: " + subject + "\nDate: Wed, 04 Nov 2026 09:30:00 +0000\nMessage-ID: <" + subject + "@example.com>\n\nbody of " + subject + "\n",
	}
}

func uids(items []map[string]any) []int {
	var out []int
	for _, it := range items {
		out = append(out, int(it["uid"].(float64)))
	}
	return out
}

func hasCommand(cmds []string, prefix string) bool {
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func gmailMessages() []imaptest.Message {
	subject := mime.BEncoding.Encode("utf-8", "Réunion à 10h")
	return []imaptest.Message{
		{
			UID: 1, GmMsgID: 0x1873f2e4a9b0c1d2, GmThrID: 1873000000000001,
			Flags:  []string{`\Flagged`},
			Labels: []string{`"\\Inbox"`, `"Work Stuff"`, `Receipts`, `"&AOk-t&AOk-"`},
			Raw: `From: =?ISO-8859-1?Q?Jos=E9?= <jose@example.com>
To: Me <me@example.com>, Bob <bob@example.com>
Cc: Carol <carol@example.com>
Subject: ` + subject + `
Date: Wed, 04 Nov 2026 09:30:00 -0500
Message-ID: <m1@example.com>
In-Reply-To: <prev@example.com>
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary=XX

--XX
Content-Type: text/plain; charset=utf-8

Hello   there,

see   you at 10.
--XX
Content-Type: text/html; charset=utf-8

<p>HTML version</p>
--XX--
`,
		},
		{
			UID: 2, GmMsgID: 0x1873f2e4a9b0c1d3, GmThrID: 1873000000000002,
			Flags: []string{`\Seen`}, FlagsAfterBody: true, Labels: []string{`"\\Sent"`},
			Raw: `From: Billing <billing@example.com>
To: me@example.com
Subject: Invoice
Date: Wed, 04 Nov 2026 10:00:00 +0000
Message-ID: <m2@example.com>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary=YY

--YY
Content-Type: text/html; charset=utf-8

<html><head><title>T</title><style>p{color:red}</style></head><body><p>Invoice &amp; receipt</p><script>evil()</script><p>Total:<br>$5</p></body></html>
--YY
Content-Type: application/pdf
Content-Disposition: attachment; filename="a.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--YY--
`,
		},
		{
			UID: 3, GmMsgID: 0x1873f2e4a9b0c1d4, GmThrID: 1873000000000003,
			Raw: "From: Old <old@example.com>\nTo: me@example.com\nSubject: =?x-bogus?Q?caf=E9?=\nDate: Wed, 04 Nov 2026 11:00:00 +0000\n" +
				"Message-ID: <m3@example.com>\nMIME-Version: 1.0\nContent-Type: text/plain; charset=x-bogus-charset\nContent-Transfer-Encoding: 8bit\n\ncaf\xe9 ok\n",
		},
		{
			UID: 4, GmMsgID: 0x1873f2e4a9b0c1d5, GmThrID: 1873000000000004,
			Raw: "From: Long <long@example.com>\nTo: me@example.com\nSubject: Long\nDate: Wed, 04 Nov 2026 12:00:00 +0000\nMessage-ID: <m4@example.com>\n\n" + strings.Repeat("a", 5000) + "\n",
		},
	}
}

func TestIMAPGmailItems(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{Gmail: true, UIDValidity: 777, User: imapUser, Password: imapPass, Messages: gmailMessages()})
	res := runIMAP(t, srv, nil)
	res.mustSucceed(t)
	if got := uids(res.items); !reflect.DeepEqual(got, []int{4, 3, 2, 1}) {
		t.Fatalf("uids %v, want newest first [4 3 2 1]\nstderr: %s", got, res.stderr)
	}
	items := byID(t, res.items)

	m1 := items[strconv.FormatUint(0x1873f2e4a9b0c1d2, 10)]
	if m1 == nil {
		t.Fatalf("no item with the X-GM-MSGID as id: %v", res.items)
	}
	wantFields(t, m1, map[string]any{
		"source":          "imap",
		"thread_id":       "1873000000000001",
		"mailbox":         "INBOX",
		"from":            "José <jose@example.com>",
		"from_address":    "jose@example.com",
		"to":              "Me <me@example.com>, Bob <bob@example.com>",
		"cc":              "Carol <carol@example.com>",
		"subject":         "Réunion à 10h",
		"date":            "2026-11-04T14:30:00Z",
		"updated_at":      "2026-11-04T14:30:00Z",
		"message_id":      "<m1@example.com>",
		"in_reply_to":     "<prev@example.com>",
		"snippet":         "Hello there, see you at 10.",
		"has_attachments": false,
		"url":             "https://mail.google.com/mail/u/0/#all/1873f2e4a9b0c1d2",
	})
	if !reflect.DeepEqual(m1["labels"], []any{`\Inbox`, "Work Stuff", "Receipts", "été"}) {
		t.Errorf("labels = %#v", m1["labels"])
	}
	if !reflect.DeepEqual(m1["flags"], []any{`\Flagged`}) {
		t.Errorf("flags = %#v", m1["flags"])
	}

	// HTML-only body is stripped to text (no style or script content); the PDF counts as an attachment;
	// FLAGS sent after the body literal are still read.
	m2 := items[strconv.FormatUint(0x1873f2e4a9b0c1d3, 10)]
	wantFields(t, m2, map[string]any{"snippet": "Invoice & receipt Total: $5", "has_attachments": true})
	if !reflect.DeepEqual(m2["flags"], []any{`\Seen`}) || !reflect.DeepEqual(m2["labels"], []any{`\Sent`}) {
		t.Errorf("flags/labels after the body literal: %#v %#v", m2["flags"], m2["labels"])
	}

	// An unknown charset must not crash the poll; undecodable bytes are replaced.
	m3 := items[strconv.FormatUint(0x1873f2e4a9b0c1d4, 10)]
	snip, _ := m3["snippet"].(string)
	if !strings.HasPrefix(snip, "caf") || !strings.HasSuffix(snip, " ok") {
		t.Errorf("bad-charset snippet = %q", snip)
	}
	if subj, _ := m3["subject"].(string); !strings.HasPrefix(subj, "caf") {
		t.Errorf("bad-charset subject = %q", subj)
	}

	if snip := items[strconv.FormatUint(0x1873f2e4a9b0c1d5, 10)]["snippet"].(string); len(snip) != 2000 {
		t.Errorf("snippet length %d, want 2000", len(snip))
	}

	// Read-only protocol use: EXAMINE, never SELECT; UIDs; BODY.PEEK; Gmail attributes.
	cmds := srv.Commands()
	if !hasCommand(cmds, `EXAMINE "INBOX"`) || hasCommand(cmds, "SELECT") || hasCommand(cmds, "STORE") {
		t.Errorf("commands %q: want EXAMINE and no SELECT/STORE", cmds)
	}
	if !hasCommand(cmds, "UID SEARCH UNSEEN") {
		t.Errorf("commands %q: want UID SEARCH UNSEEN without SINCE on the first poll", cmds)
	}
	fetches := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, "UID FETCH") {
			fetches++
			for _, want := range []string{"BODY.PEEK[]", "X-GM-MSGID", "X-GM-THRID", "X-GM-LABELS"} { //nolint:misspell // Gmail attribute name
				if !strings.Contains(c, want) {
					t.Errorf("%q lacks %s", c, want)
				}
			}
		}
	}
	if fetches != 4 {
		t.Errorf("%d fetches, want 4", fetches)
	}
}

func TestIMAPWithoutGmailExtensions(t *testing.T) {
	msgs := []imaptest.Message{simpleMsg(1, "one"), simpleMsg(2, "two")}
	msgs[1].Flags = []string{`\Seen`}
	srv := imaptest.Start(t, imaptest.Options{UIDValidity: 777, User: imapUser, Password: imapPass, Messages: msgs})
	res := runIMAP(t, srv, nil)
	res.mustSucceed(t)
	if len(res.items) != 2 {
		t.Fatalf("items: %v\nstderr: %s", res.items, res.stderr)
	}
	wantFields(t, res.items[0], map[string]any{"id": "777:2", "uid": float64(2), "url": nil, "thread_id": nil, "subject": "two"})
	wantFields(t, res.items[1], map[string]any{"id": "777:1", "url": nil, "thread_id": nil})
	for _, it := range res.items {
		if !reflect.DeepEqual(it["labels"], []any{}) {
			t.Errorf("labels = %#v, want an empty array", it["labels"])
		}
	}
	if !reflect.DeepEqual(res.items[0]["flags"], []any{`\Seen`}) {
		t.Errorf("flags = %#v", res.items[0]["flags"])
	}
	for _, c := range srv.Commands() {
		if strings.Contains(c, "X-GM-") {
			t.Errorf("asked a non-Gmail server for Gmail attributes: %q", c)
		}
	}
}

func TestIMAPSinceFollowsLastSuccess(t *testing.T) {
	for _, tc := range []struct{ name, last, search, want string }{
		{"first poll has no SINCE", "", "", "UID SEARCH UNSEEN"},
		{"one day of slack", "2026-11-05T10:00:00.123456789Z", "", "UID SEARCH (UNSEEN) SINCE 04-Nov-2026"},
		{"custom search is grouped", "2026-11-05T10:00:00Z", "FROM bob", "UID SEARCH (FROM bob) SINCE 04-Nov-2026"},
		{"month boundary", "2026-03-01T00:30:00Z", "", "UID SEARCH (UNSEEN) SINCE 28-Feb-2026"},
		{"unparseable value is ignored", "garbage", "", "UID SEARCH UNSEEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := imaptest.Start(t, imaptest.Options{User: imapUser, Password: imapPass, Messages: []imaptest.Message{simpleMsg(1, "one")}})
			res := runIMAP(t, srv, map[string]string{"HAL_LAST_SUCCESS_AT": tc.last, "IMAP_SEARCH": tc.search})
			res.mustSucceed(t)
			var got string
			for _, c := range srv.Commands() {
				if strings.HasPrefix(c, "UID SEARCH") {
					got = c
				}
			}
			if got != tc.want {
				t.Errorf("search command %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIMAPMaxKeepsNewest(t *testing.T) {
	var msgs []imaptest.Message
	for i := 1; i <= 5; i++ {
		msgs = append(msgs, simpleMsg(i, fmt.Sprintf("m%d", i)))
	}
	srv := imaptest.Start(t, imaptest.Options{Gmail: true, User: imapUser, Password: imapPass, Messages: msgs})
	res := runIMAP(t, srv, map[string]string{"IMAP_MAX": "2"})
	res.mustSucceed(t)
	if got := uids(res.items); !reflect.DeepEqual(got, []int{5, 4}) {
		t.Errorf("uids %v, want the newest two, newest first", got)
	}
	n := 0
	for _, c := range srv.Commands() {
		if strings.HasPrefix(c, "UID FETCH") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("fetched %d messages, want only the 2 kept", n)
	}
}

func TestIMAPMailboxWithSpaces(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{
		Gmail: true, User: imapUser, Password: imapPass,
		Mailboxes: []string{"INBOX", "[Gmail]/All Mail"}, Messages: []imaptest.Message{simpleMsg(1, "one")},
	})
	res := runIMAP(t, srv, map[string]string{"IMAP_MAILBOX": "[Gmail]/All Mail", "IMAP_SEARCH": "ALL"})
	res.mustSucceed(t)
	if len(res.items) != 1 || res.items[0]["mailbox"] != "[Gmail]/All Mail" {
		t.Fatalf("items: %v\nstderr: %s", res.items, res.stderr)
	}
	if !hasCommand(srv.Commands(), `EXAMINE "[Gmail]/All Mail"`) || !hasCommand(srv.Commands(), "UID SEARCH ALL") {
		t.Errorf("commands %q", srv.Commands())
	}
}

func TestIMAPErrors(t *testing.T) {
	t.Run("auth failure names the cause and never prints the password", func(t *testing.T) {
		const wrong = "wrong-secret-pw-999"
		srv := imaptest.Start(t, imaptest.Options{User: imapUser, Password: imapPass, Messages: []imaptest.Message{simpleMsg(1, "one")}})
		res := runIMAP(t, srv, map[string]string{"IMAP_PASSWORD": wrong})
		res.mustFail(t, "AUTHENTICATIONFAILED")
		if res.exit != 1 || !strings.Contains(res.stderr, "login failed") || !strings.Contains(res.stderr, "app password") {
			t.Errorf("exit %d, stderr %q", res.exit, res.stderr)
		}
		if strings.Contains(res.stderr, wrong) || strings.Contains(res.stdout, wrong) {
			t.Errorf("the password leaked:\n%s", res.stderr)
		}
		if hasCommand(srv.Commands(), "EXAMINE") {
			t.Error("the mailbox was opened after a failed login")
		}
	})

	t.Run("missing credentials point at the Environment section", func(t *testing.T) {
		for name, extra := range map[string]map[string]string{
			"no password": {"IMAP_PASSWORD": ""},
			"no user":     {"IMAP_USER": ""},
		} {
			res := runIMAP(t, nil, extra)
			res.mustFail(t, "set them in the sensor Environment section")
			if res.exit != 1 || !strings.Contains(res.stderr, "IMAP_USER") || !strings.Contains(res.stderr, "IMAP_PASSWORD") {
				t.Errorf("%s: exit %d, stderr %q", name, res.exit, res.stderr)
			}
		}
	})

	t.Run("unknown mailbox", func(t *testing.T) {
		srv := imaptest.Start(t, imaptest.Options{User: imapUser, Password: imapPass})
		res := runIMAP(t, srv, map[string]string{"IMAP_MAILBOX": "Nope"})
		res.mustFail(t, "cannot open mailbox Nope")
		if res.exit != 1 || strings.Contains(res.stderr, imapPass) {
			t.Errorf("exit %d, stderr %q", res.exit, res.stderr)
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		res := runIMAP(t, nil, map[string]string{"IMAP_HOST": "127.0.0.1", "IMAP_PORT": strconv.Itoa(port)})
		res.mustFail(t, "cannot talk to 127.0.0.1:"+strconv.Itoa(port))
		if res.exit != 1 {
			t.Errorf("exit %d", res.exit)
		}
	})

	t.Run("TLS to a plain server fails visibly", func(t *testing.T) {
		srv := imaptest.Start(t, imaptest.Options{User: imapUser, Password: imapPass})
		res := runIMAP(t, srv, map[string]string{"IMAP_TLS": "ssl"})
		if res.exit != 1 || strings.TrimSpace(res.stdout) != "" || strings.Contains(res.stderr, imapPass) || !strings.HasPrefix(res.stderr, "imap-mail:") {
			t.Errorf("exit %d, stdout %q, stderr %q", res.exit, res.stdout, res.stderr)
		}
	})
}

// The sensor definition passes spec validation, its stdout parses as sensor
// items, and the key template gives one distinct key per message that is the
// same on the next poll (so dedup drops what was already seen).
func TestIMAPSensorSpecItemsAndKeys(t *testing.T) {
	raw, err := FS.ReadFile("sensors/imap-mail.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sensor, diags := spec.ParseSensor(raw)
	if len(diags) > 0 {
		t.Fatalf("spec diagnostics: %v", diags)
	}

	srv := imaptest.Start(t, imaptest.Options{Gmail: true, UIDValidity: 777, User: imapUser, Password: imapPass, Messages: gmailMessages()})
	keys := func() []string {
		res := runIMAP(t, srv, nil)
		res.mustSucceed(t)
		items, err := runner.ParseSensorItems([]byte(res.stdout))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			dec := json.NewDecoder(bytes.NewReader(it))
			dec.UseNumber()
			var v any
			if err := dec.Decode(&v); err != nil {
				t.Fatal(err)
			}
			k, err := spec.RenderNamed("key", sensor.Key, v)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, k)
		}
		return out
	}
	first := keys()
	if len(first) != 4 {
		t.Fatalf("keys %v", first)
	}
	seen := map[string]bool{}
	for _, k := range first {
		if !regexp.MustCompile(`^\d+$`).MatchString(k) || seen[k] {
			t.Errorf("key %q is not a distinct X-GM-MSGID: %v", k, first)
		}
		seen[k] = true
	}
	if second := keys(); !reflect.DeepEqual(first, second) {
		t.Errorf("keys changed between polls: %v then %v", first, second)
	}
}

// The email-secretary prompt may only use fields the sensor emits.
func TestEmailSecretaryPromptUsesSensorFields(t *testing.T) {
	srv := imaptest.Start(t, imaptest.Options{User: imapUser, Password: imapPass, Messages: []imaptest.Message{simpleMsg(1, "one")}})
	res := runIMAP(t, srv, nil)
	res.mustSucceed(t)
	if len(res.items) != 1 {
		t.Fatalf("items %v", res.items)
	}
	prompt, err := FS.ReadFile("prompts/email-secretary.md")
	if err != nil {
		t.Fatal(err)
	}
	fields := regexp.MustCompile(`\{\{\s*\.input\.(\w+)`).FindAllStringSubmatch(string(prompt), -1)
	if len(fields) == 0 {
		t.Fatal("prompt references no input fields")
	}
	for _, f := range fields {
		if _, ok := res.items[0][f[1]]; !ok {
			t.Errorf("prompt uses .input.%s, which imap-mail does not emit", f[1])
		}
	}
	flow := loadFlows(t)["email-secretary"]
	if !reflect.DeepEqual(flow.Subscribe, []string{"imap-mail"}) {
		t.Errorf("email-secretary subscribes to %v, want [imap-mail]", flow.Subscribe)
	}
}
