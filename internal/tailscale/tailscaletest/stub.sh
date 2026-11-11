#!/bin/sh
# A stand-in for the tailscale CLI, for tests and screenshots. It never touches a real Tailscale.
# State lives in files under $TS_STUB_DIR (default: the directory baked in by tailscaletest):
#   status.json        output of `tailscale status --json`
#   dnsname            this machine's MagicDNS name (used to build `serve status --json`)
#   serve-<port>       a mapping on that HTTPS port; the file holds the proxy target
#   funnel             when present, `serve status --json` reports funnel on
#   down               when present every command fails as if tailscaled were not running
#   serve-fail         when present `tailscale serve ...` (not status) prints its content and fails
#   version            output of `tailscale version` (default 1.80.2)
#   calls.log          every invocation, one line of arguments each
d="${TS_STUB_DIR:-@STATE_DIR@}"
echo "$*" >> "$d/calls.log"
if [ -f "$d/down" ]; then
  echo "failed to connect to local tailscaled; it doesn't appear to be running" >&2
  exit 1
fi
case "$1" in
  version)
    if [ -f "$d/version" ]; then cat "$d/version"; else echo "1.80.2"; echo "  tailscale commit: 0000000000000000000000000000000000000000"; fi
    ;;
  status)
    echo 'Warning: client version "1.80.2" != tailscaled server version "1.80.2-tfake"' >&2
    cat "$d/status.json"
    ;;
  whois)
    f="$d/whois-$3.json"
    if [ -f "$f" ]; then cat "$f"; else echo "no match for IP $3" >&2; exit 1; fi
    ;;
  serve)
    if [ "$2" = "status" ]; then
      dns=$(cat "$d/dnsname" 2>/dev/null)
      tcp=""; web=""
      for f in "$d"/serve-*; do
        [ -f "$f" ] || continue
        p=${f##*serve-}
        case "$p" in fail) continue ;; esac
        t=$(cat "$f")
        tcp="$tcp${tcp:+,}\"$p\":{\"HTTPS\":true}"
        web="$web${web:+,}\"$dns:$p\":{\"Handlers\":{\"/\":{\"Proxy\":\"$t\"}}}"
      done
      fun=""
      if [ -f "$d/funnel" ]; then fun=",\"AllowFunnel\":{\"$dns:443\":true}"; fi
      if [ -z "$tcp" ]; then echo '{}'; else printf '{"TCP":{%s},"Web":{%s}%s}\n' "$tcp" "$web" "$fun"; fi
      exit 0
    fi
    if [ -f "$d/serve-fail" ]; then cat "$d/serve-fail" >&2; exit 1; fi
    port=""; target=""; off=""
    for a in "$@"; do
      case "$a" in
        --https=*) port=${a#--https=} ;;
        off) off=1 ;;
        --bg|--yes|serve) ;;
        --*) ;;
        *) target=$a ;;
      esac
    done
    [ -n "$port" ] || { echo "stub: serve needs --https=<port>" >&2; exit 2; }
    if [ -n "$off" ]; then rm -f "$d/serve-$port"; exit 0; fi
    [ -n "$target" ] || { echo "stub: serve needs a target" >&2; exit 2; }
    echo "$target" > "$d/serve-$port"
    echo "Available within your tailnet:"
    ;;
  *)
    echo "stub: unexpected: $*" >&2
    exit 2
    ;;
esac
