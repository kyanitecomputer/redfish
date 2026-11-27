// Package auth authenticates and authorizes the requests of a Redfish service
// and serves the AccountService, its accounts and roles, and the
// SessionService with its sessions.
//
// Service.Handler wraps the service's handler. A request authenticates with
// the X-Auth-Token of a session, with HTTP Basic authentication, or with a
// TLS client certificate that the server verified: its Common Name is the
// name of the account it authenticates as. The service root, the OData
// documents and the POST that creates a session need no authentication, as
// DSP0266 requires. Every other request needs a privilege of the account's
// role, as Service.Privilege decides.
//
// Logins with a password are limited: failed ones lock the user name for a
// while, at most two passwords, of logins or of password changes, are hashed
// at a time, and a request that waits too long for that gets 503. Sessions
// and client certificates need no hashing. Requests that change something
// are refused when a browser sends them from another origin
// (http.CrossOriginProtection), so that credentials a browser keeps cannot
// be used by other sites.
//
// A client certificate is accepted only from the TLS configuration of the
// http.Server: every CA in its ClientCAs may issue certificates for any
// account name, and nothing checks revocation, so use CAs of your own for
// it. The package is independent of the operating system: accounts are kept
// through a Storage, and TLS is left to the http.Server.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// URIs of the resources the package serves.
const (
	SessionServiceURI = "/redfish/v1/SessionService"
	SessionsURI       = SessionServiceURI + "/Sessions"
	AccountServiceURI = "/redfish/v1/AccountService"
	AccountsURI       = AccountServiceURI + "/Accounts"
	RolesURI          = AccountServiceURI + "/Roles"
)

// Service authenticates requests against Accounts and keeps their sessions.
// Set its fields before it serves requests.
type Service struct {
	// MaxSessions limits the open sessions; DefaultMaxSessions when zero.
	// An account may have a quarter of them: a login beyond that ends the
	// account's oldest session.
	MaxSessions int
	// CrossOrigin refuses changes that browsers send from other origins;
	// a new http.CrossOriginProtection when nil, which accepts requests
	// without an Origin header, such as those of most Redfish clients, and
	// those whose Origin is the Host they were sent to. Add the origins of
	// browser front ends that run elsewhere, or behind a proxy that
	// changes Host, with AddTrustedOrigin. Its deny handler is replaced by
	// one that answers with a Redfish error.
	CrossOrigin *http.CrossOriginProtection
	// Privilege returns the privilege a request needs, for the requests
	// outside the account and session services, whose rules are fixed;
	// DefaultPrivilege when nil.
	Privilege func(r *http.Request) Privilege

	accounts *Accounts
	sessions *sessions
	now      func() time.Time
}

// New returns a Service for the accounts a.
func New(a *Accounts) *Service {
	return &Service{accounts: a, sessions: newSessions(), now: time.Now}
}

// Accounts returns the accounts of the service.
func (s *Service) Accounts() *Accounts { return s.accounts }

// Principal is who a request authenticated as.
type Principal struct {
	Account Account
	// SessionID is the Id of the session the request used, or "" for
	// HTTP Basic or a client certificate.
	SessionID string
}

type principalKey struct{}

// PrincipalFrom returns the principal of a request that Service.Handler
// authenticated.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Handler wraps next so that it serves only authenticated requests whose
// account has the privilege they need. It answers others with 401 or 403,
// and with 503 when too many logins with a password arrive at once.
func (s *Service) Handler(next http.Handler) http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public(r) {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.authenticate(r)
		if err != nil {
			s.loginFailed(w, err)
			return
		}
		if need := s.required(r, p); !p.Account.Has(need) {
			server.WriteError(w, http.StatusForbidden, odata.MsgInsufficientPrivilege,
				"There are insufficient privileges for the account or credentials associated with the current session to perform the requested operation.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
	cop := s.CrossOrigin
	if cop == nil {
		cop = http.NewCrossOriginProtection()
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.WriteError(w, http.StatusForbidden, odata.MsgInsufficientPrivilege,
			"The request was refused: a browser sent it from another origin.")
	}))
	return cop.Handler(h)
}

// public reports whether r needs no authentication: the service root, the
// version and OData documents, and logging in.
func public(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		switch path {
		case "/redfish", "/redfish/v1", "/redfish/v1/odata", "/redfish/v1/$metadata":
			return true
		}
	case http.MethodPost:
		return path == SessionsURI
	}
	return false
}

// authenticate finds the account of r: by its session token, by HTTP Basic
// authentication, or by its verified TLS client certificate, in that order.
// A request that carries a token or Basic credentials is not authenticated
// otherwise when they fail.
func (s *Service) authenticate(r *http.Request) (Principal, error) {
	now := s.now()
	policy := s.accounts.Policy()
	if token := r.Header.Get("X-Auth-Token"); token != "" {
		sess, ok := s.sessions.lookup(token, now, policy.SessionTimeout)
		if !ok {
			return Principal{}, errLogin
		}
		// A session ends with the account, its password and its being
		// enabled; a lockout only stops logins with the password, as
		// failed logins of someone else must not end the account's
		// sessions, nor stop its certificate.
		// A session that does not match is refused but left to expire, not
		// ended: the session that changed its own password is moved to
		// the new generation only just after the change.
		acc, ok := s.accounts.Get(sess.user)
		if !ok || !acc.Enabled || acc.gen != sess.gen {
			return Principal{}, errLogin
		}
		s.sessions.touch(sess.id, now)
		return Principal{Account: acc, SessionID: sess.id}, nil
	}
	if user, password, ok := r.BasicAuth(); ok {
		if policy.BasicAuth == BasicDisabled {
			return Principal{}, errLogin
		}
		acc, err := s.accounts.Authenticate(user, password)
		return Principal{Account: acc}, err
	}
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
		acc, ok := s.accounts.Get(r.TLS.VerifiedChains[0][0].Subject.CommonName)
		if !ok || !acc.Enabled {
			return Principal{}, errLogin
		}
		return Principal{Account: acc}, nil
	}
	return Principal{}, errLogin
}

// current reports whether a session of user with generation gen still
// works: its account exists, is enabled and has that generation.
func (s *Service) current(user string, gen uint64) bool {
	acc, ok := s.accounts.Get(user)
	return ok && acc.Enabled && acc.gen == gen
}

// loginFailed answers a request whose credentials failed: 503 for ErrBusy,
// otherwise 401.
func (s *Service) loginFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrBusy) {
		w.Header().Set("Retry-After", "1")
		server.WriteError(w, http.StatusServiceUnavailable, odata.MsgServiceTemporarilyUnavailable,
			"The service is temporarily unavailable. Too many logins arrived at once; retry later.")
		return
	}
	s.unauthorized(w)
}

func (s *Service) unauthorized(w http.ResponseWriter) {
	if s.accounts.Policy().BasicAuth == BasicEnabled {
		w.Header().Set("WWW-Authenticate", `Basic realm="Redfish", charset="UTF-8"`)
	}
	server.WriteError(w, http.StatusUnauthorized, odata.MsgNoValidSession,
		"There is no valid session established with the implementation.")
}

// required returns the privilege r needs. The account and session services
// have fixed rules, which let an account read and change itself, and read
// and end its own sessions, with ConfigureSelf.
func (s *Service) required(r *http.Request, p Principal) Privilege {
	path := strings.TrimSuffix(r.URL.Path, "/")
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if rest, ok := strings.CutPrefix(path, AccountsURI+"/"); ok {
		name, action, _ := strings.Cut(rest, "/")
		self := name == p.Account.UserName
		if self && (read || r.Method == http.MethodPatch || action == "Actions/ManagerAccount.ChangePassword") {
			return ConfigureSelf
		}
		return ConfigureUsers
	}
	if under(path, AccountServiceURI) {
		if read {
			return Login
		}
		return ConfigureUsers
	}
	if id, ok := strings.CutPrefix(path, SessionsURI+"/"); ok {
		if sess, open := s.sessions.get(id, s.now(), s.accounts.Policy().SessionTimeout); open && sess.user == p.Account.UserName {
			return Login
		}
		return ConfigureManager
	}
	if under(path, SessionServiceURI) {
		if read {
			return Login
		}
		return ConfigureManager
	}
	if s.Privilege != nil {
		return s.Privilege(r)
	}
	return DefaultPrivilege(r)
}

// DefaultPrivilege returns the privilege a request outside the account and
// session services needs, following the DMTF privilege registry: Login to
// read, ConfigureManager to change or act on a manager or a task, and
// ConfigureComponents for anything else.
func DefaultPrivilege(r *http.Request) Privilege {
	switch {
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		return Login
	case under(r.URL.Path, "/redfish/v1/Managers"), under(r.URL.Path, "/redfish/v1/TaskService"):
		return ConfigureManager
	}
	return ConfigureComponents
}

// under reports whether path is the URI prefix or below it.
func under(path, prefix string) bool {
	rest, ok := strings.CutPrefix(path, prefix)
	return ok && (rest == "" || rest[0] == '/')
}
