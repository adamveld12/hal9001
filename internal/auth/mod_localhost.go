package auth

import (
	"context"
	"strings"
)

// LocalhostTrust succeeds only for requests that are provably local: the TCP
// peer is loopback, Host is localhost or 127.0.0.1, and no proxy header is
// present. tailscaled proxies from loopback, so the Host and header checks are
// what keep tailnet requests out (REQUIREMENTS §10.5).
type LocalhostTrust struct{}

func (LocalhostTrust) Name() string { return "localhost-trust" }

func (LocalhostTrust) Authenticate(_ context.Context, req *AuthRequest) Result {
	if !req.Peer.IsValid() || !req.Peer.Unmap().IsLoopback() {
		return Ignore("peer is not loopback")
	}
	if h := req.HostName(); h != "localhost" && h != "127.0.0.1" {
		return Ignore("Host is not localhost or 127.0.0.1")
	}
	for name := range req.Header {
		if h := proxyHeader(name); h != "" {
			return Ignore("proxy header present: " + h)
		}
	}
	return Success(Principal{Name: "localhost"})
}

// proxyHeader returns the canonical name when name marks a proxied request.
func proxyHeader(name string) string {
	l := strings.ToLower(name)
	if l == "forwarded" || strings.HasPrefix(l, "x-forwarded-") || strings.HasPrefix(l, "tailscale-") {
		return name
	}
	return ""
}
