// Package imaptest is a minimal in-process IMAP4 server on 127.0.0.1 for tests
// of the imap-mail starter sensor. It speaks plain text (no TLS) and implements
// only what that sensor uses: CAPABILITY, LOGIN, EXAMINE, UID SEARCH, UID FETCH
// with BODY.PEEK[] and LOGOUT. It rejects anything that could change mailbox
// state (SELECT, STORE, a non-PEEK BODY[]), and records every command.
package imaptest

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Message is one message in the fake mailbox.
type Message struct {
	UID int
	// Raw is the RFC 5322 message; "\n" is sent as CRLF.
	Raw string
	// Flags are IMAP flags such as `\Seen`.
	Flags []string
	// GmMsgID and GmThrID are sent only when the server is in Gmail mode.
	GmMsgID, GmThrID uint64
	// Labels are X-GM-LABELS astrings exactly as they go on the wire, for
	// example `"\\Inbox"`, `"Work Stuff"`, `Receipts`.
	Labels []string
	// FlagsAfterBody sends FLAGS after the message literal, as some servers do.
	FlagsAfterBody bool
}

// Options configure Start.
type Options struct {
	// Gmail advertises X-GM-EXT-1 and sends the Gmail FETCH attributes.
	Gmail       bool
	UIDValidity int
	User        string
	Password    string
	// Mailboxes that exist; empty means just INBOX.
	Mailboxes []string
	Messages  []Message
}

// Server is a running fake server.
type Server struct {
	Host string
	Port int

	opts Options
	ln   net.Listener
	mu   sync.Mutex
	cmds []string
}

// Start listens on 127.0.0.1:0 and serves until the test ends.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	if len(o.Mailboxes) == 0 {
		o.Mailboxes = []string{"INBOX"}
	}
	if o.UIDValidity == 0 {
		o.UIDValidity = 1
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &Server{Host: "127.0.0.1", Port: addr.Port, opts: o, ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

// Commands returns every command received so far, without its tag (for
// example `UID SEARCH UNSEEN`). LOGIN is recorded without its arguments.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cmds...)
}

// SetMessages replaces the mailbox content.
func (s *Server) SetMessages(msgs []Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.Messages = msgs
}

func (s *Server) record(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds = append(s.cmds, cmd)
}

func (s *Server) messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.opts.Messages...)
}

var (
	// An IMAP astring argument: a quoted string or an atom.
	astring  = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|([^\s"]+)`)
	unescape = regexp.MustCompile(`\\(.)`)
	fetchUID = regexp.MustCompile(`^UID FETCH (\d+) \((.*)\)$`)
)

func (s *Server) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	w := bufio.NewWriter(c)
	say := func(format string, a ...any) {
		_, _ = fmt.Fprintf(w, format+"\r\n", a...)
	}
	say("* OK [CAPABILITY IMAP4rev1] fake IMAP ready")
	_ = w.Flush()

	r := bufio.NewReader(c)
	authed, examined := false, false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		tag, cmd, _ := strings.Cut(line, " ")
		name, args, _ := strings.Cut(cmd, " ")
		name = strings.ToUpper(name)
		if name == "LOGIN" {
			s.record("LOGIN")
		} else {
			s.record(cmd)
		}
		switch name {
		case "CAPABILITY":
			caps := "IMAP4rev1 UIDPLUS"
			if s.opts.Gmail {
				caps += " X-GM-EXT-1"
			}
			say("* CAPABILITY %s", caps)
			say("%s OK CAPABILITY completed", tag)
		case "LOGIN":
			var got []string
			for _, m := range astring.FindAllStringSubmatch(args, -1) {
				if m[2] != "" {
					got = append(got, m[2])
				} else {
					got = append(got, unescape.ReplaceAllString(m[1], "$1"))
				}
			}
			if len(got) != 2 || got[0] != s.opts.User || got[1] != s.opts.Password {
				say("%s NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)", tag)
				break
			}
			authed = true
			say("%s OK LOGIN completed", tag)
		case "EXAMINE":
			if !authed {
				say("%s BAD not authenticated", tag)
				break
			}
			box := unescape.ReplaceAllString(strings.Trim(args, `"`), "$1")
			if !contains(s.opts.Mailboxes, box) {
				say("%s NO [NONEXISTENT] Unknown Mailbox: %s", tag, box)
				break
			}
			examined = true
			say("* %d EXISTS", len(s.messages()))
			say("* OK [UIDVALIDITY %d] UIDs valid", s.opts.UIDValidity)
			say("%s OK [READ-ONLY] EXAMINE completed", tag)
		case "SELECT", "STORE", "COPY", "EXPUNGE", "APPEND":
			say("%s NO %s is not allowed: the sensor must stay read-only", tag, name)
		case "UID":
			s.uid(say, tag, cmd, examined)
		case "LOGOUT":
			say("* BYE logging out")
			say("%s OK LOGOUT completed", tag)
			_ = w.Flush()
			return
		default:
			say("%s BAD unknown command %s", tag, name)
		}
		_ = w.Flush()
	}
}

func (s *Server) uid(say func(string, ...any), tag, cmd string, examined bool) {
	if !examined {
		say("%s BAD no mailbox open", tag)
		return
	}
	switch {
	case strings.HasPrefix(cmd, "UID SEARCH "):
		var ids []string
		for _, m := range s.messages() {
			ids = append(ids, strconv.Itoa(m.UID))
		}
		say("* SEARCH %s", strings.Join(ids, " "))
		say("%s OK SEARCH completed", tag)
	case strings.HasPrefix(cmd, "UID FETCH "):
		m := fetchUID.FindStringSubmatch(cmd)
		if m == nil {
			say("%s BAD cannot parse FETCH", tag)
			return
		}
		items := strings.Fields(m[2])
		if !contains(items, "BODY.PEEK[]") {
			say("%s BAD only BODY.PEEK[] may fetch the body: a plain BODY[] marks the message read", tag)
			return
		}
		for i, msg := range s.messages() {
			if strconv.Itoa(msg.UID) != m[1] {
				continue
			}
			s.fetch(say, i+1, msg)
		}
		say("%s OK FETCH completed", tag)
	default:
		say("%s BAD unsupported UID command", tag)
	}
}

func (s *Server) fetch(say func(string, ...any), seq int, m Message) {
	raw := strings.ReplaceAll(strings.ReplaceAll(m.Raw, "\r\n", "\n"), "\n", "\r\n")
	head := fmt.Sprintf("UID %d", m.UID)
	if s.opts.Gmail {
		head += fmt.Sprintf(" X-GM-MSGID %d X-GM-THRID %d X-GM-LABELS (%s)", m.GmMsgID, m.GmThrID, strings.Join(m.Labels, " ")) //nolint:misspell // Gmail attribute name
	}
	flags := fmt.Sprintf(" FLAGS (%s)", strings.Join(m.Flags, " "))
	if !m.FlagsAfterBody {
		head += flags
		flags = ""
	}
	// The literal is followed by the remaining attributes and the closing paren.
	say("* %d FETCH (%s BODY[] {%d}\r\n%s%s)", seq, head, len(raw), raw, flags)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
