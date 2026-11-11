package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/auth"
	"hal9001/internal/authcfg"
	"hal9001/internal/httpsec"
	"hal9001/internal/spec"
)

// ---- sign-in settings (/api/auth/overview, methods, password, allowed-logins, stacks) ----
//
// Every edit of the auth section of config.yaml goes through planAuthChange
// (comment-preserving YAML edit, the config validators, the lock-out rule and
// the confirmation guardrails) and commitAuthChange (write, reload, end sessions,
// auth event). The handlers differ only in what they want to change.

// Auth event actions of these endpoints.
const (
	actionMethodsChange  = "auth_methods_change"
	actionPasswordSet    = "auth_password_set"
	actionPasswordChange = "auth_password_change"
	actionPasswordRemove = "auth_password_remove"
	actionLoginsChange   = "auth_logins_change"
	actionStacksChange   = "auth_stacks_change"
)

const (
	typeConfirmationRequired = httpsec.ProblemBase + "confirmation-required"
	// codeLocalhostTrustRequired is the diagnostic of an edit that would lock this Mac out.
	codeLocalhostTrustRequired = "localhost_trust_required"

	maxAllowedLogins = 100
	maxLoginBytes    = 254
)

// ---- views ----

type authMethodView struct {
	ID         string `json:"id"`
	Module     string `json:"module"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Enabled    bool   `json:"enabled"`
	Toggleable bool   `json:"toggleable"`
	Custom     bool   `json:"custom"`
	Detail     string `json:"detail"`
}

type authCurrentView struct {
	Method    *string  `json:"method"`
	Modules   []string `json:"modules"`
	Remote    bool     `json:"remote"`
	Source    string   `json:"source"`
	Principal string   `json:"principal"`
	SessionID *string  `json:"session_id"`
}

type passwordStatusView struct {
	Set        bool       `json:"set"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type authOverviewView struct {
	Methods       []authMethodView   `json:"methods"`
	Current       authCurrentView    `json:"current"`
	Password      passwordStatusView `json:"password"`
	PasskeyCount  int                `json:"passkey_count"`
	AllowedLogins []string           `json:"allowed_logins"`
}

type authChangeView struct {
	Overview      authOverviewView `json:"overview"`
	EndedSessions int              `json:"ended_sessions"`
	Notice        *string          `json:"notice"`
}

// authCaller is who is asking: the identity Require attached plus where the
// request came from.
type authCaller struct {
	id    auth.Identity
	local bool
	// module is the first module that admitted the request ("" when none is known).
	module string
}

func (s *Server) callerOf(r *http.Request) authCaller {
	id, _ := auth.IdentityFrom(r.Context())
	c := authCaller{id: id, local: s.opts.Auth.IsLocal(r)}
	if len(id.Modules) > 0 {
		c.module = id.Modules[0]
	}
	return c
}

// method is the contract method id of the caller's way in, or nil.
func (c authCaller) method() *string {
	if m, ok := authcfg.MethodByModule(c.module); ok {
		return &m.ID
	}
	return nil
}

// ownSession is the caller's session as far as the guardrails need it.
func (c authCaller) ownSession() (auth.Session, bool) {
	if c.id.Source != "session" || c.id.SessionID == "" {
		return auth.Session{}, false
	}
	return auth.Session{ID: c.id.SessionID, Principal: c.id.Principal.Name, Modules: c.id.Modules}, true
}

func (s *Server) authOverview(ctx context.Context, c authCaller, cfg *spec.Config) (authOverviewView, error) {
	passkeys, err := s.opts.Auth.PasskeyCount(ctx)
	if err != nil {
		return authOverviewView{}, err
	}
	lastUsed, err := s.opts.Auth.LastPasswordLogin(ctx)
	if err != nil {
		return authOverviewView{}, err
	}
	a := cfg.Auth
	v := authOverviewView{
		Methods: make([]authMethodView, 0, len(authcfg.Methods)),
		Current: authCurrentView{
			Method: c.method(), Modules: nonNil(slices.Clone(c.id.Modules)), Remote: !c.local, Source: c.id.Source,
			Principal: c.id.Principal.Name,
		},
		Password:      passwordStatusView{Set: authcfg.PasswordHash(a) != "", LastUsedAt: lastUsed},
		PasskeyCount:  passkeys,
		AllowedLogins: nonNil(slices.Clone(cfg.AllowedLogins)),
	}
	if c.id.SessionID != "" {
		sid := c.id.SessionID
		v.Current.SessionID = &sid
	}
	for _, m := range authcfg.Methods {
		enabled, custom := authcfg.Shape(a, m.Module)
		mv := authMethodView{ID: m.ID, Module: m.Module, Name: m.Name, Enabled: enabled, Toggleable: m.Toggleable, Custom: custom}
		mv.State, mv.Detail = methodState(m, enabled, cfg, v)
		v.Methods = append(v.Methods, mv)
	}
	if enabled, _ := authcfg.Shape(a, authcfg.ModulePassword); enabled {
		v.Password.Enabled = true
	}
	return v, nil
}

// methodState is the row state and its one-line explanation.
func methodState(m authcfg.Method, enabled bool, cfg *spec.Config, v authOverviewView) (state, detail string) {
	switch m.ID {
	case "localhost":
		if !enabled {
			return "off", "Not in the sign-in stacks: this Mac's own browser and CLI have to sign in too."
		}
		return "on", "This Mac's own browser and CLI are let in without signing in."
	case "tailscale":
		switch {
		case !enabled:
			return "off", "Devices on your tailnet cannot sign in this way."
		case !slices.ContainsFunc(cfg.AllowedHosts, isTailnetHost):
			return "attention", "This Mac's tailnet name is not in allowed_hosts: share hal on your tailnet first."
		case !cfg.Tailscale.Serve:
			return "attention", "Sharing on your tailnet is off, so no tailnet device can reach hal."
		case len(cfg.AllowedLogins) == 0:
			return "on", "Tailnet devices signed in as this Mac's owner are let in."
		default:
			return "on", "Tailnet devices signed in as " + strings.Join(cfg.AllowedLogins, ", ") + " are let in."
		}
	case "passkeys":
		switch {
		case !enabled:
			return "off", "Passkeys cannot be used to sign in."
		case v.PasskeyCount == 0:
			return "attention", "No passkey is registered yet: add one below."
		case v.PasskeyCount == 1:
			return "on", "1 passkey is registered."
		default:
			return "on", fmt.Sprintf("%d passkeys are registered.", v.PasskeyCount)
		}
	default:
		switch {
		case !enabled:
			return "off", "Password sign-in is off."
		case !v.Password.Set:
			return "attention", "Password sign-in is on but no password is set."
		default:
			return "on", "A password is set."
		}
	}
}

func isTailnetHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.HasSuffix(h, ".ts.net")
}

// ---- config file ----

// authFile is config.yaml as the edit starts from it.
type authFile struct {
	raw    []byte
	hash   string
	exists bool
	cfg    spec.Config
	diags  []spec.Diagnostic
}

// loadAuthFile reads and parses config.yaml. It answers a problem itself and
// returns false when the file cannot be read or does not parse.
func (s *Server) loadAuthFile(w http.ResponseWriter, r *http.Request) (*authFile, bool) {
	raw, hash, exists, err := s.configFile()
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	cfg, ds := spec.CheckConfig(raw)
	if cfg == nil {
		validationProblem(w, "config.yaml does not parse; fix it before changing how you sign in", ds)
		return nil, false
	}
	return &authFile{raw: raw, hash: hash, exists: exists, cfg: *cfg, diags: ds}, true
}

// authInput says what an edit wants.
type authInput struct {
	// desired is the auth section after the edit (the current one when only logins change).
	desired spec.Auth
	// logins, when non-nil, replaces allowed_logins.
	logins *[]string
	// removed lists the modules the edit takes out of the sign-in stacks; their sessions end.
	removed []string
	// endMatch, when set, replaces the sessions-of-removed-modules rule.
	endMatch func(auth.Session) bool
	// endWhy says what the sessions that end have in common ("signed in with the password").
	endWhy string
	// extraReasons are guardrail reasons the handler computed.
	extraReasons []string
}

// authChange is a planned edit.
type authChange struct {
	updated []byte
	changed bool
	after   spec.Config
	// diags are findings to report: errors this edit introduces (they block it) and auth warnings.
	diags []spec.Diagnostic
	// blocked is true when diags holds an error.
	blocked  bool
	reasons  []string
	ends     bool
	endMatch func(auth.Session) bool
	endWhy   string
}

// planAuthChange computes what an edit does without writing anything.
func (s *Server) planAuthChange(ctx context.Context, c authCaller, f *authFile, in authInput) (*authChange, error) {
	ch := &authChange{after: f.cfg, updated: f.raw}
	updated, changed, err := spec.EditConfigYAML(f.raw, func(root *yaml.Node) (bool, error) {
		a, err := authcfg.Apply(root, f.cfg.Auth, in.desired)
		if err != nil {
			return false, err
		}
		l := false
		if in.logins != nil {
			if l, err = authcfg.SetAllowedLogins(root, *in.logins); err != nil {
				return false, err
			}
		}
		return a || l, nil
	})
	if err != nil {
		return nil, err
	}
	if !changed {
		return ch, nil
	}
	ch.updated, ch.changed = updated, true

	after, ds := spec.CheckConfig(updated)
	if after == nil {
		ch.diags, ch.blocked = ds, true
		return ch, nil
	}
	ch.after = *after
	introduced := newErrors(f.diags, ds)
	ch.diags = introduced
	for _, d := range ds {
		if d.Severity != spec.SeverityError && strings.HasPrefix(d.Path, "auth") {
			ch.diags = append(ch.diags, d)
		}
	}
	if len(introduced) > 0 {
		ch.blocked = true
		return ch, nil
	}

	newCfg := authcfg.AuthConfig(after)
	if err := s.opts.Auth.Validate(newCfg); err != nil {
		ch.diags = append(ch.diags, spec.Diagnostic{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "auth", Message: err.Error()})
		ch.blocked = true
		return ch, nil
	}

	// Lock-out rule: this Mac must keep getting in. A local request may not
	// take that away; a remote one may, once it has confirmed.
	was := s.opts.Auth.LocalAdmitted(ctx, authcfg.AuthConfig(&f.cfg))
	now := s.opts.Auth.LocalAdmitted(ctx, newCfg)
	var lost []auth.Service
	for _, svc := range was {
		if !slices.Contains(now, svc) {
			lost = append(lost, svc)
		}
	}
	if len(lost) > 0 {
		if c.local {
			for _, svc := range lost {
				ch.diags = append(ch.diags, spec.Diagnostic{
					Severity: spec.SeverityError, Code: codeLocalhostTrustRequired, Path: "auth.services." + string(svc),
					Message: fmt.Sprintf("this Mac's own browser and CLI would no longer be let in to the %s service: keep localhost-trust in its stack", svc),
				})
			}
			ch.blocked = true
			return ch, nil
		}
		ch.reasons = append(ch.reasons, "This Mac's own browser and CLI will have to sign in too.")
	}

	ch.endMatch, ch.endWhy = in.endMatch, in.endWhy
	if ch.endMatch == nil {
		removed := in.removed
		ch.endMatch = func(sess auth.Session) bool {
			return slices.ContainsFunc(sess.Modules, func(m string) bool { return slices.Contains(removed, m) })
		}
		ch.endWhy = "signed in with " + viaOf(removed)
	}
	if own, ok := c.ownSession(); ok && ch.endMatch(own) {
		ch.ends = true
		ch.reasons = append(ch.reasons, "This ends the session you are using now, so you will have to sign in again.")
	}
	if _, isSession := c.ownSession(); !isSession && c.module != "" && slices.Contains(in.removed, c.module) {
		via, _ := authcfg.MethodByModule(c.module)
		ch.reasons = append(ch.reasons, fmt.Sprintf("This device is let in by %s; after the change it has to sign in another way.", via.Via))
	}
	if !c.local {
		hadRemote := slices.ContainsFunc(authcfg.AdminModules(f.cfg.Auth), authcfg.IsRemoteModule)
		hasRemote := slices.ContainsFunc(authcfg.AdminModules(in.desired), authcfg.IsRemoteModule)
		if hadRemote && !hasRemote {
			ch.reasons = append(ch.reasons, "No way in from another device would be left: nothing but this Mac could open hal.")
		}
	}
	ch.reasons = append(ch.reasons, in.extraReasons...)
	return ch, nil
}

// viaOf names the methods behind modules: "the password and a passkey".
func viaOf(modules []string) string {
	var names []string
	for _, mod := range modules {
		if m, ok := authcfg.MethodByModule(mod); ok {
			names = append(names, m.Via)
		}
	}
	switch len(names) {
	case 0:
		return "a removed method"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// confirmationRequired answers 409 with what the change does.
func confirmationRequired(w http.ResponseWriter, reasons []string, ends bool) {
	problem(w, http.StatusConflict, typeConfirmationRequired, "Confirmation required",
		"this change affects how you sign in: repeat it with confirm: true to go ahead",
		map[string]any{"reasons": reasons, "ends_session": ends})
}

// commitAuthChange applies a planned edit: it answers the problems itself and
// returns false, or writes config.yaml, reloads, ends the sessions the change
// ends, logs the auth event and returns how many sessions ended.
func (s *Server) commitAuthChange(w http.ResponseWriter, r *http.Request, c authCaller, f *authFile, ch *authChange, confirm bool, action, reason string) (int, bool) {
	if ch.blocked {
		validationProblem(w, "the change would break the sign-in configuration", ch.diags)
		return 0, false
	}
	if len(ch.reasons) > 0 && !confirm {
		confirmationRequired(w, ch.reasons, ch.ends)
		return 0, false
	}
	expected := f.hash
	if !f.exists {
		expected = ""
	}
	if _, err := spec.WriteFile(s.opts.Home, spec.KindConfig, "", ch.updated, expected); err != nil {
		if errors.Is(err, spec.ErrConflict) {
			_, cur, _, _ := s.configFile()
			fileConflict(w, "config.yaml changed while you were editing; reload and try again", f.hash, cur)
			return 0, false
		}
		s.writeError(w, r, err)
		return 0, false
	}
	if err := s.opts.Auth.Reload(authcfg.AuthConfig(&ch.after)); err != nil {
		s.log().Error("api: auth reload after a settings change failed", "err", err)
	}
	s.reload()
	s.publishFileChange(spec.KindConfig, "")

	ended, err := s.opts.Auth.EndSessions(r.Context(), ch.endMatch)
	if err != nil {
		s.log().Error("api: ending sessions after a sign-in change failed", "err", err)
	}
	if c.id.SessionID != "" && slices.Contains(ended, c.id.SessionID) {
		s.opts.Auth.ClearSessionCookie(w, r)
	}
	if len(ended) > 0 {
		reason += fmt.Sprintf("; ended %d session(s) %s", len(ended), ch.endWhy)
	}
	s.opts.Auth.RecordChange(r, action, auth.OutcomeSuccess, reason)
	return len(ended), true
}

// respondAuthChange writes the 200 AuthChangeResult.
func (s *Server) respondAuthChange(w http.ResponseWriter, r *http.Request, c authCaller, cfg *spec.Config, ended int, ownEnded bool, endWhy string) {
	ov, err := s.authOverview(r.Context(), c, cfg)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res := authChangeView{Overview: ov, EndedSessions: ended}
	if ended > 0 {
		n := fmt.Sprintf("Ended %d session%s that %s.", ended, plural(ended), endWhy)
		if ownEnded {
			n += " This includes the one you were using."
		}
		res.Notice = &n
	}
	writeJSON(w, http.StatusOK, res)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// authEdit runs one edit end to end: plan, commit, respond. reason names the change for the auth event.
func (s *Server) authEdit(w http.ResponseWriter, r *http.Request, c authCaller, f *authFile, in authInput, confirm bool, action, reason string) {
	ch, err := s.planAuthChange(r.Context(), c, f, in)
	if err != nil {
		fieldProblem(w, "", err.Error())
		return
	}
	if !ch.changed {
		s.respondAuthChange(w, r, c, &f.cfg, 0, false, "")
		return
	}
	ended, ok := s.commitAuthChange(w, r, c, f, ch, confirm, action, reason)
	if !ok {
		return
	}
	s.respondAuthChange(w, r, c, &ch.after, ended, c.id.SessionID != "" && ch.ends, ch.endWhy)
}

// ---- GET /api/auth/overview ----

func (s *Server) getAuthOverview(w http.ResponseWriter, r *http.Request) {
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	ov, err := s.authOverview(r.Context(), s.callerOf(r), &f.cfg)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

// ---- PATCH /api/auth/methods ----

func (s *Server) updateAuthMethods(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tailscale *bool `json:"tailscale"`
		Passkeys  *bool `json:"passkeys"`
		Password  *bool `json:"password"`
		Confirm   bool  `json:"confirm"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Tailscale == nil && req.Passkeys == nil && req.Password == nil {
		fieldProblem(w, "", "set at least one of tailscale, passkeys and password")
		return
	}
	s.authEditLock.Lock()
	defer s.authEditLock.Unlock()
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	c := s.callerOf(r)

	desired := authcfg.Clone(f.cfg.Auth)
	var removed, changes []string
	for _, m := range []struct {
		name, module string
		want         *bool
	}{
		{"tailscale", authcfg.ModuleTailscale, req.Tailscale},
		{"passkeys", authcfg.ModulePasskeys, req.Passkeys},
		{"password", authcfg.ModulePassword, req.Password},
	} {
		if m.want == nil {
			continue
		}
		enabled, _ := authcfg.Shape(desired, m.module)
		switch {
		case *m.want:
			if !enabled && m.module == authcfg.ModulePassword && authcfg.PasswordHash(desired) == "" {
				conflictProblem(w, typeInvalidState, "no password is set: set one first (PUT /api/auth/password turns password sign-in on)", nil)
				return
			}
			// Also completes a method that only some of the stacks the services use have.
			if next := authcfg.Enable(desired, m.module); !authcfg.Equal(next, desired) {
				desired = next
				changes = append(changes, m.name+": on")
			}
		case !*m.want && enabled:
			desired = authcfg.Disable(desired, m.module)
			removed = append(removed, m.module)
			changes = append(changes, m.name+": off")
		}
	}
	reason := "no change"
	if len(changes) > 0 {
		reason = strings.Join(changes, ", ")
	}
	s.authEdit(w, r, c, f, authInput{desired: desired, removed: removed}, req.Confirm, actionMethodsChange, reason)
}

// ---- PUT/DELETE /api/auth/password ----

// checkCurrentPassword verifies current_password against the stored hash,
// counting wrong attempts in the limiter that password logins share. It answers
// the problem itself and returns false when the attempt is refused.
func (s *Server) checkCurrentPassword(w http.ResponseWriter, r *http.Request, hash string, given *string, action string) bool {
	if wait := s.opts.Auth.PasswordRetryAfter(r); wait > 0 {
		s.opts.Auth.RecordChange(r, action, auth.OutcomeFail, "too many failed password attempts from this address")
		auth.WriteRateLimited(w, wait)
		return false
	}
	if given == nil || *given == "" {
		fieldProblem(w, "current_password", "the current password is required")
		return false
	}
	ok, err := auth.VerifyPassword(hash, *given)
	if err != nil {
		s.log().Warn("api: the stored password hash cannot be verified", "err", err)
	}
	if err != nil || !ok {
		s.opts.Auth.NotePasswordFailure(r)
		s.opts.Auth.RecordChange(r, action, auth.OutcomeFail, "wrong current password")
		fieldProblem(w, "current_password", "the current password is wrong")
		return false
	}
	s.opts.Auth.NotePasswordSuccess(r)
	return true
}

func (s *Server) setAuthPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password        string  `json:"password"`
		CurrentPassword *string `json:"current_password"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		fieldProblem(w, "password", err.Error())
		return
	}
	s.authEditLock.Lock()
	defer s.authEditLock.Unlock()
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	c := s.callerOf(r)
	action, reason := actionPasswordSet, "password set"
	if hash := authcfg.PasswordHash(f.cfg.Auth); hash != "" {
		action, reason = actionPasswordChange, "password changed"
		if !s.checkCurrentPassword(w, r, hash, req.CurrentPassword, action) {
			return
		}
	}
	newHash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	own := c.id.SessionID
	in := authInput{
		desired: authcfg.WithPasswordHash(f.cfg.Auth, newHash),
		endMatch: func(sess auth.Session) bool {
			return sess.ID != own && slices.Contains(sess.Modules, authcfg.ModulePassword)
		},
		endWhy: "signed in with the old password",
	}
	s.authEdit(w, r, c, f, in, false, action, reason)
}

func (s *Server) removeAuthPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword *string `json:"current_password"`
		Confirm         bool    `json:"confirm"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	s.authEditLock.Lock()
	defer s.authEditLock.Unlock()
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	c := s.callerOf(r)
	if hash := authcfg.PasswordHash(f.cfg.Auth); hash != "" {
		if !s.checkCurrentPassword(w, r, hash, req.CurrentPassword, actionPasswordRemove) {
			return
		}
	}
	in := authInput{
		desired: authcfg.Disable(f.cfg.Auth, authcfg.ModulePassword),
		removed: []string{authcfg.ModulePassword},
	}
	s.authEdit(w, r, c, f, in, req.Confirm, actionPasswordRemove, "password removed")
}

// ---- PUT /api/auth/allowed-logins ----

// normalizeLogins trims, validates and de-duplicates (ignoring case) a login
// list; the diagnostics name every bad entry.
func normalizeLogins(in []string) ([]string, []spec.Diagnostic) {
	var ds []spec.Diagnostic
	bad := func(i int, msg string) {
		ds = append(ds, spec.Diagnostic{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: fmt.Sprintf("logins[%d]", i), Message: msg})
	}
	if len(in) > maxAllowedLogins {
		ds = append(ds, spec.Diagnostic{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "logins", Message: fmt.Sprintf("at most %d logins are allowed", maxAllowedLogins)})
	}
	out := []string{}
	seen := map[string]bool{}
	for i, raw := range in {
		l := strings.TrimSpace(raw)
		switch at := strings.Index(l, "@"); {
		case l == "":
			bad(i, "a login must not be empty")
		case len(l) > maxLoginBytes:
			bad(i, fmt.Sprintf("a login must be at most %d bytes", maxLoginBytes))
		case strings.ContainsFunc(l, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == ',' }):
			bad(i, "a login must not contain spaces, commas or control characters")
		case at <= 0 || at == len(l)-1 || strings.Count(l, "@") != 1:
			bad(i, "a login must look like name@domain")
		default:
			if key := strings.ToLower(l); !seen[key] {
				seen[key] = true
				out = append(out, l)
			}
		}
	}
	return out, ds
}

// loginRule answers whether a Tailscale login would be admitted by a login list.
type loginRule struct {
	logins     []string
	owner      string
	ownerKnown bool
}

func (s *Server) newLoginRule(ctx context.Context, logins []string) loginRule {
	rule := loginRule{logins: logins}
	if len(logins) == 0 {
		owner, err := s.opts.Auth.OwnerLogin(ctx)
		rule.owner, rule.ownerKnown = owner, err == nil && owner != ""
	}
	return rule
}

// allows reports whether login is admitted; known is false when an empty list
// has to fall back on the Mac's owner and the owner cannot be determined.
func (l loginRule) allows(login string) (allowed, known bool) {
	if len(l.logins) > 0 {
		return slices.ContainsFunc(l.logins, func(a string) bool { return strings.EqualFold(a, login) }), true
	}
	if !l.ownerKnown {
		return false, false
	}
	return strings.EqualFold(l.owner, login), true
}

func (s *Server) setAllowedLogins(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Logins  *[]string `json:"logins"`
		Confirm bool      `json:"confirm"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Logins == nil {
		badRequest(w, "logins is required")
		return
	}
	logins, ds := normalizeLogins(*req.Logins)
	if len(ds) > 0 {
		validationProblem(w, "some logins are not valid", ds)
		return
	}
	s.authEditLock.Lock()
	defer s.authEditLock.Unlock()
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	c := s.callerOf(r)
	rule := s.newLoginRule(r.Context(), logins)
	in := authInput{
		desired: f.cfg.Auth,
		logins:  &logins,
		endMatch: func(sess auth.Session) bool {
			allowed, known := rule.allows(sess.Principal)
			return known && !allowed && slices.Contains(sess.Modules, authcfg.ModuleTailscale)
		},
		endWhy: "signed in as a login that is no longer allowed",
	}
	if _, isSession := c.ownSession(); !isSession && c.module == authcfg.ModuleTailscale {
		switch allowed, known := rule.allows(c.id.Principal.Name); {
		case !known:
			in.extraReasons = append(in.extraReasons, "hal cannot tell which login owns this Mac right now, so it cannot check that you would still be let in.")
		case !allowed:
			in.extraReasons = append(in.extraReasons, fmt.Sprintf("You are signed in over the tailnet as %s, who would no longer be let in.", c.id.Principal.Name))
		}
	}
	reason := "allowed_logins: this Mac's owner only"
	if len(logins) > 0 {
		reason = "allowed_logins: " + strings.Join(logins, ", ")
	}
	s.authEdit(w, r, c, f, in, req.Confirm, actionLoginsChange, reason)
}

// ---- GET/PUT /api/auth/stacks and POST /api/auth/stacks/preview ----

type authStacksView struct {
	Stacks   map[string][]spec.AuthModule `json:"stacks"`
	Services map[string]string            `json:"services"`
	Hash     string                       `json:"hash"`
}

type authStacksRequest struct {
	Stacks       map[string][]spec.AuthModule `json:"stacks"`
	Services     map[string]string            `json:"services"`
	ExpectedHash *string                      `json:"expected_hash"`
	Confirm      bool                         `json:"confirm"`
}

type authStacksPreviewView struct {
	Valid           bool              `json:"valid"`
	Diagnostics     []spec.Diagnostic `json:"diagnostics"`
	Changed         bool              `json:"changed"`
	ConfirmRequired bool              `json:"confirm_required"`
	Reasons         []string          `json:"reasons"`
	EndsSession     bool              `json:"ends_session"`
	Before          string            `json:"before"`
	After           string            `json:"after"`
}

// redactAuth returns a copy of a that is safe to return: the values of
// sensitive option keys (the password hash) are replaced.
func redactAuth(a spec.Auth) spec.Auth {
	out := authcfg.Clone(a)
	for name, entries := range out.Stacks {
		for i := range entries {
			if entries[i].Options != nil {
				entries[i].Options = redactAny(entries[i].Options).(map[string]any)
			}
		}
		if entries == nil {
			entries = []spec.AuthModule{}
		}
		out.Stacks[name] = entries
	}
	return out
}

func (s *Server) getAuthStacks(w http.ResponseWriter, r *http.Request) {
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	a := redactAuth(f.cfg.Auth)
	writeJSON(w, http.StatusOK, authStacksView{Stacks: a.Stacks, Services: a.Services, Hash: f.hash})
}

// containsRedacted reports whether v holds the redaction marker at any depth.
func containsRedacted(v any) bool {
	switch t := v.(type) {
	case string:
		return t == redacted
	case map[string]any:
		for _, e := range t {
			if containsRedacted(e) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(t, containsRedacted)
	}
	return false
}

// desiredStacks builds the auth section a request asks for: option values
// that are «redacted» keep the stored value, and the section is validated by
// the validator that loads the config. Findings that are errors block the edit.
func desiredStacks(f *authFile, req *authStacksRequest) (spec.Auth, []spec.Diagnostic) {
	var ds []spec.Diagnostic
	errAt := func(code, path, msg string) {
		ds = append(ds, spec.Diagnostic{Severity: spec.SeverityError, Code: code, Path: path, Message: msg})
	}
	desired := spec.Auth{Stacks: map[string][]spec.AuthModule{}, Services: map[string]string{}}
	if len(req.Stacks) == 0 {
		errAt(spec.CodeRequired, "auth.stacks", "define at least one stack")
	}
	for name, entries := range req.Stacks {
		out := make([]spec.AuthModule, len(entries))
		for i, e := range entries {
			e.Options = copyOptions(e.Options)
			for key, v := range e.Options {
				path := fmt.Sprintf("auth.stacks.%s[%d].options.%s", name, i, key)
				switch {
				case e.Module == authcfg.ModulePassword && key == authcfg.PasswordHashOption && v != redacted && !validHashOption(v):
					errAt(spec.CodeInvalidValue, path, "the password hash must be an argon2id string; set a password with PUT /api/auth/password instead of writing a hash")
				case v == redacted:
					if stored, ok := storedOption(f.cfg.Auth, name, i, e.Module, key); ok {
						e.Options[key] = stored
					} else {
						errAt(spec.CodeInvalidValue, path, "there is no stored value to keep for «redacted»; send the value itself")
					}
				case containsRedacted(v):
					errAt(spec.CodeInvalidValue, path, "«redacted» can only stand for a whole option value")
				}
			}
			out[i] = e
		}
		desired.Stacks[name] = out
	}
	for svc, stack := range req.Services {
		desired.Services[svc] = stack
	}
	for _, svc := range spec.Services {
		if desired.Services[svc] == "" {
			errAt(spec.CodeRequired, "auth.services."+svc, "name the stack that guards the "+svc+" service")
		}
	}
	if len(ds) > 0 {
		return desired, ds
	}
	tmp := f.cfg.Clone()
	tmp.Auth = desired
	for _, d := range spec.ValidateConfig(&tmp) {
		if strings.HasPrefix(d.Path, "auth") && d.Severity == spec.SeverityError {
			d.File = ""
			ds = append(ds, d)
		}
	}
	return desired, ds
}

func validHashOption(v any) bool {
	s, ok := v.(string)
	return ok && auth.ValidatePasswordHash(s) == nil
}

func copyOptions(o map[string]any) map[string]any {
	if o == nil {
		return nil
	}
	out := make(map[string]any, len(o))
	for k, v := range o {
		out[k] = v
	}
	return out
}

// storedOption finds the stored value of option key for the entry at index i
// of stack: the entry at that position when it is the same module, else the
// first entry of that module in the stack, else in any stack.
func storedOption(a spec.Auth, stack string, i int, module, key string) (any, bool) {
	entries := a.Stacks[stack]
	if i < len(entries) && entries[i].Module == module {
		if v, ok := entries[i].Options[key]; ok {
			return v, true
		}
	}
	names := make([]string, 0, len(a.Stacks))
	for name := range a.Stacks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range append([]string{stack}, names...) {
		for _, e := range a.Stacks[name] {
			if v, ok := e.Options[key]; ok && e.Module == module {
				return v, true
			}
		}
	}
	return nil, false
}

// removedFromAdmin lists the modules the admin stack loses.
func removedFromAdmin(before, after spec.Auth) []string {
	now := authcfg.AdminModules(after)
	var out []string
	for _, m := range authcfg.AdminModules(before) {
		if !slices.Contains(now, m) {
			out = append(out, m)
		}
	}
	return out
}

// stacksSummary says which stacks and services an edit touches, for the auth event.
func stacksSummary(before, after spec.Auth) string {
	var stacks, services []string
	names := map[string]bool{}
	for n := range before.Stacks {
		names[n] = true
	}
	for n := range after.Stacks {
		names[n] = true
	}
	for n := range names {
		if !authcfg.Equal(spec.Auth{Stacks: map[string][]spec.AuthModule{n: before.Stacks[n]}}, spec.Auth{Stacks: map[string][]spec.AuthModule{n: after.Stacks[n]}}) {
			stacks = append(stacks, n)
		}
	}
	for _, svc := range spec.Services {
		if before.Services[svc] != after.Services[svc] {
			services = append(services, svc)
		}
	}
	sort.Strings(stacks)
	return "stacks changed: " + strings.Join(stacks, ", ") + "; services changed: " + strings.Join(services, ", ")
}

func (s *Server) setAuthStacks(w http.ResponseWriter, r *http.Request) {
	var req authStacksRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.ExpectedHash != nil && !expectedHashOK(w, *req.ExpectedHash) {
		return
	}
	s.authEditLock.Lock()
	defer s.authEditLock.Unlock()
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	if req.ExpectedHash != nil && *req.ExpectedHash != f.hash {
		fileConflict(w, "config.yaml changed since you loaded the stacks", *req.ExpectedHash, f.hash)
		return
	}
	desired, ds := desiredStacks(f, &req)
	if len(ds) > 0 {
		validationProblem(w, "the stacks are not valid", ds)
		return
	}
	in := authInput{
		desired: desired,
		removed: removedFromAdmin(f.cfg.Auth, desired),
	}
	s.authEdit(w, r, s.callerOf(r), f, in, req.Confirm, actionStacksChange, stacksSummary(f.cfg.Auth, desired))
}

func (s *Server) previewAuthStacks(w http.ResponseWriter, r *http.Request) {
	var req authStacksRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	f, ok := s.loadAuthFile(w, r)
	if !ok {
		return
	}
	desired, ds := desiredStacks(f, &req)
	view := authStacksPreviewView{Diagnostics: []spec.Diagnostic{}, Reasons: []string{}}
	var err error
	if view.Before, err = authSectionYAML(redactAuth(f.cfg.Auth)); err != nil {
		s.writeError(w, r, err)
		return
	}
	if view.After, err = authSectionYAML(redactAuth(desired)); err != nil {
		s.writeError(w, r, err)
		return
	}
	view.Changed = !authcfg.Equal(f.cfg.Auth, desired)
	if len(ds) > 0 {
		view.Diagnostics = ds
		writeJSON(w, http.StatusOK, view)
		return
	}
	ch, err := s.planAuthChange(r.Context(), s.callerOf(r), f, authInput{desired: desired, removed: removedFromAdmin(f.cfg.Auth, desired)})
	if err != nil {
		view.Diagnostics = []spec.Diagnostic{{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "auth", Message: err.Error()}}
		writeJSON(w, http.StatusOK, view)
		return
	}
	view.Valid = !ch.blocked
	view.Diagnostics = append(view.Diagnostics, ch.diags...)
	view.ConfirmRequired, view.EndsSession = len(ch.reasons) > 0, ch.ends
	view.Reasons = append(view.Reasons, ch.reasons...)
	writeJSON(w, http.StatusOK, view)
}

// authSectionYAML renders the auth section the way config.yaml holds it.
func authSectionYAML(a spec.Auth) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(struct {
		Auth spec.Auth `yaml:"auth"`
	}{a}); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
