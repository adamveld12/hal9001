# Authentication

hal9001 listens on `127.0.0.1:9001` only; the address is fixed. Remote access goes through Tailscale (see
[Tailscale](#tailscale)). On top of that fixed network baseline, who may do what is decided by PAM-style stacks in
`config.yaml`.

## Network baseline (not configurable)

Applied to every request before auth:

- `Host` must be `localhost:9001`, `127.0.0.1:9001`, or an entry of `allowed_hosts`; otherwise `403 host-not-allowed`
  (DNS-rebinding protection).
- An `Origin` header, when present, must be an allowed origin (`http` for the loopback hosts, `https` for other allowed
  hosts); otherwise `403 origin-not-allowed`. No CORS headers are ever sent.
- Mutating requests (POST, PUT, PATCH, DELETE) must be `Content-Type: application/json`; otherwise `415`. A cross-site
  form cannot send that without a preflight, which the server never grants.
- `POST /mcp` (the remote MCP endpoint, [mcp.md](mcp.md)) is stricter: the TCP peer, `Host` and `Origin` must be loopback
  and proxy headers (`Forwarded`, `X-Forwarded-*`, `Tailscale-*`) are refused (`403`), even for an `allowed_hosts` entry.
- Session cookies (`hal9001_session`) are `HttpOnly`, `SameSite=Strict`, and `Secure` over HTTPS.

## Services

Each API operation declares the service it needs (`x-hal-service` in [`api/openapi.yaml`](../api/openapi.yaml)):

| Service | Covers |
|---|---|
| `ui` | Loading the web UI and establishing a browser session. |
| `api` | Read endpoints, the event stream and the MCP endpoint `POST /mcp` (its tools then call the REST API with the same credentials, so `flow_run` and `sensor_poll` need `admin` like the endpoints they call). |
| `approve` | Approving and denying proposals and tool calls. |
| `admin` | Writing flows, sensors, prompts, env and settings; kill switch; event injection, replay and test-node; runtime probes. **Equivalent to running code on this Mac.** |

## Stacks

`config.yaml` defines named stacks, and maps each service to one. Without an `auth` section the default is used:

```yaml
auth:
  stacks:
    default:
      - {module: localhost-trust, control: sufficient}
      - {module: tailscale-identity, control: sufficient}
      - {module: webauthn, control: sufficient}
      - {module: password, control: sufficient}
  services: {ui: default, api: default, approve: default, admin: default}
```

A stack is an ordered list of `{module, control, options}`. Each module returns one of: success (with a principal),
fail, **ignore** (not applicable to this request, counts as neither) or challenge (needs another step in an interactive
login). Controls follow PAM:

| Control | On success | On failure |
|---|---|---|
| `required` | continue | remember the failure, continue, fail at the end |
| `requisite` | continue | fail immediately |
| `sufficient` | succeed immediately, unless an earlier `required` failed | continue (ignored) |
| `optional` | counts only if it is the only module that ran | ignored |

If the end of the stack is reached: any `required` failure fails; otherwise at least one success succeeds; otherwise it
fails (`no module succeeded`). Unknown module names and unknown service or stack names are rejected by validation
(`hal9001 validate`).

### Examples

Lock down local access, keeping Tailscale and passkey or password:

```yaml
auth:
  stacks:
    default:
      - {module: tailscale-identity, control: sufficient}
      - {module: webauthn, control: sufficient}
      - {module: password, control: sufficient}
```

Open for reading from localhost, but approvals and admin need a password even there (separate stacks):

```yaml
auth:
  stacks:
    open:
      - {module: localhost-trust, control: sufficient}
    strict:
      - {module: password, control: required}
  services: {ui: open, api: open, approve: strict, admin: strict}
```

A session grants every service that maps to the stack that authenticated it (no step-up), so keep services that need
different credentials on different stacks, as above.

Fail fast with `requisite`: a password that is presented and wrong is rejected even from localhost, instead of falling
through to `localhost-trust` (with no password presented the module returns `ignore` and evaluation continues):

```yaml
auth:
  stacks:
    default:
      - {module: password, control: requisite}
      - {module: localhost-trust, control: sufficient}
```

Evaluation is visible in the auth events: a `requisite` failure stops evaluation (later modules do not appear); a
`required` failure followed by a `sufficient` success still fails.

Changes to `config.yaml` are picked up without a restart.

## Modules

| Module | Behavior |
|---|---|
| `localhost-trust` | Success when the request is provably local: the TCP peer is loopback, `Host` is `localhost` or `127.0.0.1`, and no proxy header is present (`Forwarded`, `X-Forwarded-*`, `Tailscale-*`). Otherwise `ignore`. Principal `localhost`. |
| `tailscale-identity` | Applies only to requests that came through `tailscale serve`; see [Tailscale](#tailscale). Principal is the Tailscale login. |
| `webauthn` | Passkeys (Touch ID through the browser). Interactive logins only. Registered by any caller the `admin` stack admits. Principal `owner`. |
| `password` | One argon2id hash in `config.yaml`. Interactive logins only. Principal `owner`. |

`webauthn` and `password` never authenticate a plain API request: they take part in a login
(`POST /api/auth/{service}/begin`) that yields a session cookie.

### password

```sh
bin/hal9001 auth set-password              # prompts without echo
printf %s "$PW" | bin/hal9001 auth set-password --stdin   # reads the first line of stdin
```

This writes the argon2id hash (`$argon2id$v=19$m=65536,t=3,p=4$...`) as the `hash` option of **every** `password` entry
in the auth stacks of `config.yaml`, keeping comments and layout (if there is no `auth` section the default stack is
added). The command fails if no stack has a `password` entry; add one first. It uses the same optimistic-write check as
the API, so it refuses to overwrite a concurrent edit. The server uses the hash of the first `password` entry that has
one (services in the order `ui`, `api`, `approve`, `admin`).

```yaml
- {module: password, control: sufficient, options: {hash: "$argon2id$v=19$m=65536,t=3,p=4$..."}}
```

Treat `config.yaml` as sensitive after this; `GET /api/settings` and `hal9001 settings get` redact it.

### webauthn

- The relying-party ID is the request hostname, so `localhost` and each Tailscale hostname hold **separate**
  credentials: register a passkey once per hostname you use. IP addresses (`127.0.0.1`) cannot be used.
- Registration is under `/api/auth/webauthn/register/{begin,finish}` (and in the UI under Settings). Any caller the
  `admin` service admits can register, including a browser admitted by `localhost-trust`; begin and finish must come from
  the same caller (session, or stack principal).
- User verification is required. Credentials can be listed and removed with `/api/auth/webauthn/credentials`.
- Passkeys on `localhost` only matter when `localhost-trust` is not in the stack.

## Logging in from scripts

When the stack does not admit a request, the API answers `401 unauthenticated` with `service` and `methods` (the
interactive modules of that stack). Log in, keep the cookie, and pass it on:

```sh
H='Content-Type: application/json'
curl -sS -c jar -X POST -H "$H" http://localhost:9001/api/auth/admin/begin \
  -d '{"method": "password", "password": "..."}'
curl -sS -b jar http://localhost:9001/api/status

# the CLI takes the cookie too:
export HAL9001_SESSION_COOKIE="hal9001_session=$(awk '$6=="hal9001_session"{print $7}' jar)"
bin/hal9001 status
```

[`docs/api.md`](api.md#auth-model) documents the exchange (`begin`, `continue` for multi-step challenges such as a
passkey assertion, `logout`). A session lasts `session_ttl` (default 7d, sliding). Other endpoints:

| Endpoint | Purpose |
|---|---|
| `GET /api/auth/whoami` | Who the server thinks you are (always 200). |
| `POST /api/auth/logout` | End the current session. |
| `GET /api/auth/sessions`, `DELETE /api/auth/sessions`, `DELETE /api/auth/sessions/{id}` | List, revoke all, revoke one. |
| `GET /api/auth/events` | Authentication decisions. |
| `GET /api/auth/tailscale` | The same report as `hal9001 tailscale status`. |

The last three need the `admin` service.

## Tailscale

The daemon stays loopback-only; Tailscale does the exposure. Never use `tailscale funnel`, which publishes to the
internet. hal9001 never runs `tailscale serve` itself.

1. Install Tailscale on this Mac and log in.
2. Publish the loopback port to your tailnet with a valid TLS certificate:

   ```sh
   tailscale serve --bg --https=443 http://127.0.0.1:9001
   ```

   The Mac is now reachable at `https://<mac>.<tailnet>.ts.net` from devices on your tailnet only.
3. Let the server accept that hostname in `config.yaml`:

   ```yaml
   allowed_hosts: [my-mac.my-tailnet.ts.net]
   # allowed_logins: [you@example.com]   # optional; see below
   ```

4. Check the setup:

   ```sh
   bin/hal9001 tailscale status --table
   ```

   It reports whether the `tailscale` CLI and daemon are reachable, this Mac's tailnet hostname and owner login, whether
   serve proxies to port 9001, whether `funnel` is enabled (it should not be), whether the hostname is in
   `allowed_hosts`, and the effective `allowed_logins`.

`localhost-trust` does not treat these requests as local. tailscaled proxies from loopback, so the peer address alone
would pass; the Host and proxy-header conditions exclude them. Tailnet requests therefore fall through to
`tailscale-identity`, then `webauthn` or `password`.

### tailscale-identity

Always registered and in the default stack. It applies only when `Host` is a `.ts.net` hostname listed in
`allowed_hosts` **and** a `Tailscale-User-Login` header is present; otherwise it returns `ignore`. Then:

1. It takes the tailnet client IP from `X-Forwarded-For` (set by `tailscale serve`; if several hops are listed, the last
   one).
2. It runs `tailscale whois --json <ip>` and requires the returned user login to equal the header. Tagged devices have
   no user and fail.
3. It requires the login to be in `allowed_logins`. When that list is empty, the only allowed login is the one that owns
   this Mac (`tailscale status --json`, `Self.UserID`).

On success the principal is the login. If the `tailscale` CLI or daemon is unavailable the module returns `ignore` and
the reason is recorded in the auth events. Whois and owner lookups are cached for 60 seconds.

Outcomes: header login matches whois and is allowed: success. Header login differs from whois: fail. Login not in
`allowed_logins`: fail. Tagged device: fail. `Host` is not an allowed ts.net hostname, or no header: ignore.

With `tailscale-identity` removed from the stack, tailnet devices must use a passkey registered for the ts.net hostname
(WebAuthn works there because it is HTTPS) or the password.

## Threat notes

- **Admin is code execution.** Anyone who passes the `admin` stack can write a flow or sensor that runs arbitrary
  commands as you. Treat every module in that stack accordingly.
- **Local processes pass `localhost-trust`.** Any process on this Mac that connects to `127.0.0.1:9001` is trusted in the
  default stack, including with a forged `Host: localhost:9001`. The module is there so the CLI and your browser need no
  login. To lock local processes out, remove `localhost-trust`; also remove `tailscale-identity`, or accept the next
  point.
- **Forged Tailscale headers.** `tailscale serve` sets the identity headers and is expected to overwrite client-supplied
  ones (REQUIREMENTS scenario 24 lists a live check). A local process can forge headers on loopback, but it would have to
  name a real tailnet IP owned by an allowed login, and local processes already pass `localhost-trust` in the default
  stack, so the module adds no access for them.
- **Websites cannot reach it.** The Host check defeats DNS rebinding; the Origin check and the JSON-only rule defeat
  cross-site requests.
- **Never expose it by other means.** There is no setting to bind another address. Do not proxy it to the internet; do
  not use `tailscale funnel` (`hal9001 tailscale status` flags it).
- **Secrets.** The secret values of the `env` file are never returned by the API, and are redacted from stored output. A value the user marked plain (`# hal:plain`) is returned and not redacted. The password
  hash lives in `config.yaml`, which is why config files are redacted in `settings get`.

## Auth events

Every authentication decision is stored with the service, endpoint, each module's result (module, control, result,
reason), the outcome, principal, remote address, host and session id. Actions are `request`, `whoami`, `login_begin`,
`login_continue`, `logout`, `session_revoke`, `session_revoke_all`, `webauthn_register` and `webauthn_delete`. Read them
in the UI (Settings) or `GET /api/auth/events` (service `admin`), and live on the event stream as `auth_event.created`.

```sh
curl -sS http://localhost:9001/api/auth/events | jq '.events[] | {ts, service, outcome, principal, modules}'
```
