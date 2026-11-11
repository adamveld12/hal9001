# Authentication

hal9001 listens on `127.0.0.1:9001` only; the address is fixed. Remote access goes through Tailscale, which hal9001 can
set up itself (see [Tailscale](#tailscale)). On top of that fixed network baseline, who may do what is decided by PAM-style stacks in
`config.yaml`.

## Network baseline (not configurable)

Applied to every request before auth:

- `Host` must be `localhost:9001`, `127.0.0.1:9001`, or an entry of `allowed_hosts`; otherwise `403 host-not-allowed`
  (DNS-rebinding protection).
- An `Origin` header, when present, must be an allowed origin (`http` for the loopback hosts, `https` for other allowed
  hosts); otherwise `403 origin-not-allowed`. No CORS headers are ever sent.
- A request carrying `Tailscale-Funnel-Request` (tailscaled adds it to requests that arrive through `tailscale
  funnel`, from the public internet) is refused with `403 forbidden` on every path, before the Host check and before
  auth. The `localhost-trust` module also ignores any request with a `Tailscale-*` header.
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
added). When no stack a service uses has a `password` entry, the command adds `{module: password, control: sufficient}`
to each of them, which turns password sign-in on. The password must be at least 12 characters, as in the API; a shorter
one is refused and nothing is written. It uses the same optimistic-write check as the API, so it refuses to overwrite a
concurrent edit. The server uses the hash of the first `password` entry that has one (services in the order `ui`, `api`,
`approve`, `admin`).

```yaml
- {module: password, control: sufficient, options: {hash: "$argon2id$v=19$m=65536,t=3,p=4$..."}}
```

Treat `config.yaml` as sensitive after this; `GET /api/settings`, `GET /api/auth/stacks` and `hal9001 settings get`
redact the hash.

### webauthn

- The relying-party ID is the request hostname, so `localhost` and each Tailscale hostname hold **separate**
  credentials: register a passkey once per hostname you use. IP addresses (`127.0.0.1`) cannot be used.
- Registration is under `/api/auth/webauthn/register/{begin,finish}` (and in the UI under Settings). Any caller the
  `admin` service admits can register, including a browser admitted by `localhost-trust`; begin and finish must come from
  the same caller (session, or stack principal).
- User verification is required. Credentials can be listed and removed with `/api/auth/webauthn/credentials`.
- Passkeys on `localhost` only matter when `localhost-trust` is not in the stack.

## The Auth page

**Settings > Auth** ("How you sign in to hal") is the one place to see and change how hal admits you. The old
separate Tailscale settings page is gone; `/settings/tailscale` redirects to `/settings/auth#tailscale`. The page has these
cards:

| Card | What it shows and does |
|---|---|
| Overview | One row per way in: **On this Mac** (`localhost-trust`), **From your tailnet** (`tailscale-identity`), **Passkeys** (`webauthn`) and **Password** (`password`). Each is on, off or needs attention (for example sharing is off, no passkey is registered, or no password is set), with a switch. On this Mac has no switch. |
| Tailscale | Sharing status, warnings with the command that fixes each, **Share on your tailnet** (and stop sharing), the URL, the funnel refusal, `allowed_hosts`, and the editable **Who can sign in** list (`allowed_logins`). |
| Password | Set, change or remove the password; when it was last used; only the argon2id hash is stored. |
| Passkeys | Register and remove passkeys. |
| Sessions | List and end sessions. |
| Sign-in events | Recent authentication decisions ([Auth events](#auth-events)). |
| Advanced | Collapsed. The auth stacks and the service mapping as an editor, with validation and a diff before saving. |

![The Overview card](images/auth/overview-light.png#gh-light-mode-only)
![The Overview card](images/auth/overview-dark.png#gh-dark-mode-only)

![The Tailscale card: sharing and Who can sign in](images/auth/tailscale-light.png#gh-light-mode-only)
![The Tailscale card: sharing and Who can sign in](images/auth/tailscale-dark.png#gh-dark-mode-only)

Password card, before a password is set, and after (set and change form):

![The Password card, no password set](images/auth/password-notset-light.png#gh-light-mode-only)
![The Password card, no password set](images/auth/password-notset-dark.png#gh-dark-mode-only)

![The Password card, set, with the change form](images/auth/password-set-light.png#gh-light-mode-only)
![The Password card, set, with the change form](images/auth/password-set-dark.png#gh-dark-mode-only)

The Advanced card shows the diff of the `auth:` section before it writes anything:

![The stacks editor with a diff](images/auth/advanced-light.png#gh-light-mode-only)
![The stacks editor with a diff](images/auth/advanced-dark.png#gh-dark-mode-only)

## Changing sign-in from the UI or API

The page uses these endpoints. All need the `admin` service, and every change is logged as an auth event with the
action shown.

| Endpoint | Purpose | Auth event action |
|---|---|---|
| `GET /api/auth/overview` | One row per way in (`localhost`, `tailscale`, `passkeys`, `password`) with `state` `on`, `off` or `attention`, plus `current`: how this request was admitted. Never returns the hash. | none |
| `PATCH /api/auth/methods` | Turn `tailscale`, `passkeys` or `password` on or off. | `auth_methods_change` |
| `PUT /api/auth/password` | Set or change the password. | `auth_password_set`, `auth_password_change` |
| `DELETE /api/auth/password` | Remove the password. | `auth_password_remove` |
| `PUT /api/auth/allowed-logins` | Replace `allowed_logins`. | `auth_logins_change` |
| `GET /api/auth/stacks` | The stacks and service mapping (hash redacted), and the config `hash` to send back as `expected_hash`. | none |
| `POST /api/auth/stacks/preview` | Validate a stacks change and return the `before` and `after` YAML, writing nothing. | none |
| `PUT /api/auth/stacks` | Write the stacks and service mapping. | `auth_stacks_change` |

Switching a method on adds `{module, control: sufficient}` to every stack a service uses that lacks it; off removes the
module from every stack. Edits keep the comments of `config.yaml`. The method `localhost` cannot be switched.

### Guardrails

The server enforces these; the UI only shows them.

- On this Mac (`localhost-trust`) can never be turned off, whoever asks. Any change that would stop a plain local
  request from being admitted to any service (switching methods, `PUT /api/auth/stacks` including the advanced editor,
  moving a service to a stack without it, and so on) is refused with `422` and the diagnostic
  `localhost_trust_required`, nothing is written. This holds for remote callers too, and `confirm: true` does not bypass
  it. The preview reports it as an error finding.
- A change needs `confirm: true` when it disables the method this request or session was admitted by, or leaves a remote
  request with no remote method (`tailscale`, `passkeys`, `password`). Without it the server answers
  `409 confirmation-required` with `reasons` and `ends_session`, and changes
  nothing. Repeat the request with `confirm: true` to go ahead. The UI shows this as a confirm dialog.
- A change ends the sessions that were established with a method it removes. Turning a method off ends its sessions;
  `PUT /api/auth/stacks` ends sessions whose method no longer appears in the `admin` stack. When the caller's own session
  ends, the response clears the cookie.

![The confirm dialog](images/auth/confirm-light.png#gh-light-mode-only)
![The confirm dialog](images/auth/confirm-dark.png#gh-dark-mode-only)

### Password

- At least 12 characters (`422` otherwise). Stored as an argon2id PHC string in the `hash` option of the `password`
  entries in the stacks. The password and the hash are never returned, and settings views show `«redacted»`.
- **Set** stores the hash and, when no stack a service uses has a `password` entry, adds one (which turns password
  sign-in on). **Change** needs `current_password` (`422` when wrong) and ends every other session established with the
  password; the caller's own session stays. **Remove** needs `current_password`, deletes the hash and the `password`
  entries (password sign-in is then off) and ends every password session. It needs `confirm: true` when it would end the
  caller's own session or is the last remote method of a remote caller. Removing when no password is set does nothing.
- Turning `password` on with `PATCH /api/auth/methods` needs a password to be set first (`409 invalid-state`); turning it
  off deletes the hash.
- Wrong `current_password` values and failed password logins share one limit per peer address: after 5 failures within
  15 minutes, further attempts get `429 rate-limited` with `Retry-After`. A success clears the count. Behind `tailscale
  serve` every tailnet client reaches hal9001 from the same loopback address, so they share one counter.

### Who can sign in

`PUT /api/auth/allowed-logins` replaces `allowed_logins`. An empty list means the login that owns this Mac only. Each
entry must look like `name@domain` (no spaces, commas or control characters, at most 254 bytes). Duplicates
(case-insensitive) are dropped. `422` lists every bad entry. When the request was admitted by `tailscale-identity` and
the new list no longer admits the caller's own login, it needs `confirm: true`.

### Stacks editor

`GET /api/auth/stacks` returns the effective `auth.stacks` and `auth.services` (defaults filled in). `PUT` writes them
and must pass the same validation as a config load (unknown modules, controls, services or stacks give `422` with
diagnostics) plus the guardrails above. Comments and unchanged entries stay. A `«redacted»` option value keeps the stored
one, so the hash survives an edit. `409 conflict` means `expected_hash` is stale: the file changed, reload.
`POST /api/auth/stacks/preview` runs every check and writes nothing; it returns `valid`, `diagnostics`,
`confirm_required`, `reasons`, `ends_session`, and the `before` and `after` YAML of the `auth:` section (secrets
redacted), which the UI diffs.

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

Both need the `admin` service. The Tailscale endpoints (`GET /api/tailscale`, `POST /api/tailscale/refresh`,
`POST` and `DELETE /api/tailscale/serve`) are in [`api/openapi.yaml`](../api/openapi.yaml) and need `admin`.

## Tailscale

The daemon stays loopback-only; Tailscale does the exposure, and hal9001 sets it up for you. It never uses `tailscale
funnel`, which publishes to the internet: sharing is for devices on your own tailnet.

1. Install Tailscale on this Mac and sign in. Your tailnet needs MagicDNS and HTTPS certificates turned on (under DNS in
   the [admin console](https://login.tailscale.com/admin/dns)).
2. In the web UI open **Settings > Auth** (the Tailscale card) and press **Share on your tailnet**, or run:

   ```sh
   bin/hal9001 tailscale serve              # --port 8443 for another HTTPS port, --replace to take over a busy one
   ```

   hal9001 runs `tailscale serve --bg --yes --https=443 http://127.0.0.1:9001` (the `--bg` mapping survives restarts of
   Tailscale and of hal9001), adds this Mac's MagicDNS name to `allowed_hosts` in `config.yaml`, and sets
   `tailscale: {serve: true, https_port: 443}`. The Mac is then reachable at `https://<mac>.<tailnet>.ts.net` from
   devices on your tailnet only.
3. Check it any time:

   ```sh
   bin/hal9001 tailscale status --table
   ```

   It reports the `tailscale` CLI (found on `PATH`, in Homebrew's directories or in the Tailscale app) and its version,
   whether you are signed in, this Mac's name and tailnet, whether HTTPS certificates are on, what the HTTPS port serves
   and whether it points at hal9001, whether funnel is on (it should not be), whether the name is in `allowed_hosts`,
   the logins `tailscale-identity` accepts, and a warning with the fix (a command to copy, or a link) for each problem.

What the button does, exactly:

| Step | Command or edit |
|---|---|
| Share | `tailscale serve --bg --yes --https=<port> http://127.0.0.1:<hal9001 port>` (default port 443). Nothing is run when the port already points at hal9001. |
| Funnel on the port | `tailscale funnel` exposes the HTTPS port to the public internet: hal9001 refuses (`409 invalid-state`, also with `--replace` and also when the port already points at hal9001), runs nothing and does not touch `allowed_hosts` or `tailscale.serve`. The Tailscale card shows the error and disables the button; run `tailscale funnel --https=<port> off` (or pick another port). At start the same rule applies: a funnel port is not re-served and is reported as the `funnel_on_port` warning. |
| Port busy | The HTTPS port already serves something else: hal9001 refuses (`409`; the UI asks, the CLI wants `--replace`) and never overwrites it silently. |
| `allowed_hosts` | Adds `<name>` (or `<name>:<port>` when the port is not 443, matching the `Host` header a browser sends) unless it is already there. Comments and your own entries stay; there are no duplicates. |
| Stop sharing | `tailscale serve --https=<port> off`, only when the mapping on that port points at hal9001, and `tailscale.serve: false`. The `allowed_hosts` entry is removed only if hal9001 added it (recorded in `<home>/data/tailscale.json`); entries you wrote stay. |

At start, while `tailscale.serve` is `true`, hal9001 checks the mapping and puts it back if Tailscale lost it (for
example after `tailscale serve reset` or a Tailscale reinstall). It waits for tailscaled when it is still starting, never
replaces a mapping to something else, and shows a failure as a warning in the Tailscale card and in the log. If this
Mac's tailnet name changed, the new name is added to `allowed_hosts`; the old entry stays and shows as a notice, for you
to remove from `config.yaml`.

Who may sign in over the tailnet is unchanged: `tailscale-identity` (below) accepts the logins in `allowed_logins`, or
the login that owns this Mac when the list is empty. The Tailscale card shows who that is and lets you edit the list
(**Who can sign in**). Sharing does not change the auth stacks; remove `tailscale-identity` and tailnet devices need a passkey or the
password instead.

Manual setups (a hostname hal9001 does not manage, a reverse proxy of your own): run your own `tailscale serve`, and add
the name by hand to `allowed_hosts` and, if needed, `allowed_logins`:

```yaml
allowed_hosts: [my-mac.my-tailnet.ts.net]
```

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
  not use `tailscale funnel`. hal9001 does not share on an HTTPS port that funnel exposes (it would make the host name
  reachable from the internet), and it refuses every request that carries `Tailscale-Funnel-Request`, so a funnel that
  points at hal9001 gets `403` from the daemon. `hal9001 tailscale status` flags both (`funnel_enabled`,
  `funnel_on_port`). Funnel requests carry no tailnet identity, so `tailscale-identity` could not admit them either,
  and `localhost-trust` ignores any request with a `Tailscale-*` header or a non-localhost Host.
- **Secrets.** The secret values of the `env` file are never returned by the API, and are redacted from stored output. A value the user marked plain (`# hal:plain`) is returned and not redacted. The password
  hash lives in `config.yaml`, which is why config files are redacted in `settings get`.

## Auth events

Every authentication decision is stored with the service, endpoint, each module's result (module, control, result,
reason), the outcome, principal, remote address, host and session id. Actions are `request`, `whoami`, `login_begin`,
`login_continue`, `logout`, `session_revoke`, `session_revoke_all`, `webauthn_register`, `webauthn_delete`,
`auth_methods_change`, `auth_password_set`, `auth_password_change`, `auth_password_remove`, `auth_logins_change` and
`auth_stacks_change`. Read them in Settings > Auth (Sign-in events) or `GET /api/auth/events` (service `admin`), and
live on the event stream as `auth_event.created`.

```sh
curl -sS http://localhost:9001/api/auth/events | jq '.events[] | {ts, service, outcome, principal, modules}'
```
