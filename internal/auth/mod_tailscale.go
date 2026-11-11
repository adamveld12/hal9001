package auth

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"hal9001/internal/tailscale"
)

// whoisCacheTTL is how long a whois (and owner-login) lookup is reused.
const whoisCacheTTL = 60 * time.Second

// TailscaleIdentity signs in requests that arrived through `tailscale serve`
// by checking the identity headers against `tailscale whois`
// (REQUIREMENTS §10.4).
type TailscaleIdentity struct {
	ts  tailscale.Client
	now func() time.Time

	mu     sync.Mutex
	whois  map[netip.Addr]whoisEntry
	owner  ownerEntry
	hasOwn bool
}

type whoisEntry struct {
	who     tailscale.Whois
	expires time.Time
}

type ownerEntry struct {
	login   string
	expires time.Time
}

// NewTailscaleIdentity builds the module over ts. now may be nil.
func NewTailscaleIdentity(ts tailscale.Client, now func() time.Time) *TailscaleIdentity {
	if now == nil {
		now = time.Now
	}
	return &TailscaleIdentity{ts: ts, now: now, whois: map[netip.Addr]whoisEntry{}}
}

func (*TailscaleIdentity) Name() string { return "tailscale-identity" }

func (m *TailscaleIdentity) Authenticate(ctx context.Context, req *AuthRequest) Result {
	host := req.HostName()
	if !strings.HasSuffix(host, ".ts.net") || !slices.ContainsFunc(req.Settings.AllowedHosts, func(h string) bool {
		return strings.EqualFold(hostName(h), host)
	}) {
		return Ignore("Host is not an allowed ts.net hostname")
	}
	rawLogin := req.Header.Get("Tailscale-User-Login")
	if rawLogin == "" {
		return Ignore("no Tailscale-User-Login header")
	}
	login, err := new(mime.WordDecoder).DecodeHeader(rawLogin)
	if err != nil {
		login = rawLogin
	}

	ip, err := forwardedClientIP(req)
	if err != nil {
		return Fail(err.Error())
	}
	who, err := m.lookup(ctx, ip)
	if errors.Is(err, tailscale.ErrUnavailable) {
		return Ignore(err.Error())
	}
	if err != nil {
		return Fail(fmt.Sprintf("whois %s: %v", ip, err))
	}
	if len(who.Node.Tags) > 0 {
		return Fail(fmt.Sprintf("%s is a tagged device (%s) and has no user", ip, strings.Join(who.Node.Tags, ",")))
	}
	whoLogin := who.Login()
	if whoLogin == "" {
		return Fail(fmt.Sprintf("whois returned no user for %s", ip))
	}
	if !strings.EqualFold(whoLogin, login) {
		return Fail(fmt.Sprintf("Tailscale-User-Login %q does not match whois login %q for %s", login, whoLogin, ip))
	}

	allowed := req.Settings.AllowedLogins
	if len(allowed) == 0 {
		owner, err := m.ownerLogin(ctx)
		if errors.Is(err, tailscale.ErrUnavailable) {
			return Ignore(err.Error())
		}
		if err != nil {
			return Fail(fmt.Sprintf("cannot determine this Mac's owner: %v", err))
		}
		if owner == "" {
			return Fail("cannot determine this Mac's owner: status has no Self.UserID login")
		}
		allowed = []string{owner}
	}
	if !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, whoLogin) }) {
		return Fail(fmt.Sprintf("login %q is not in allowed_logins", whoLogin))
	}
	return Success(Principal{Name: whoLogin})
}

// forwardedClientIP returns the tailnet client IP set by tailscale serve in
// X-Forwarded-For. serve overwrites the header, but if several hops are listed
// the last one is the one our nearest proxy appended, so that is the one used.
func forwardedClientIP(req *AuthRequest) (netip.Addr, error) {
	values := req.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return netip.Addr{}, errors.New("missing X-Forwarded-For")
	}
	parts := strings.Split(values[len(values)-1], ",")
	raw := strings.TrimSpace(parts[len(parts)-1])
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid X-Forwarded-For %q", raw)
	}
	return ip.Unmap(), nil
}

func (m *TailscaleIdentity) lookup(ctx context.Context, ip netip.Addr) (tailscale.Whois, error) {
	now := m.now()
	m.mu.Lock()
	e, ok := m.whois[ip]
	m.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.who, nil
	}
	who, err := m.ts.Whois(ctx, ip)
	if err != nil {
		return tailscale.Whois{}, err
	}
	m.mu.Lock()
	m.whois[ip] = whoisEntry{who: who, expires: now.Add(whoisCacheTTL)}
	m.mu.Unlock()
	return who, nil
}

func (m *TailscaleIdentity) ownerLogin(ctx context.Context) (string, error) {
	now := m.now()
	m.mu.Lock()
	o, ok := m.owner, m.hasOwn
	m.mu.Unlock()
	if ok && now.Before(o.expires) {
		return o.login, nil
	}
	st, err := m.ts.Status(ctx)
	if err != nil {
		return "", err
	}
	login := st.OwnerLogin()
	m.mu.Lock()
	m.owner, m.hasOwn = ownerEntry{login: login, expires: now.Add(whoisCacheTTL)}, true
	m.mu.Unlock()
	return login, nil
}
