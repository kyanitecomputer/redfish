package auth

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// Register wires the SessionService and its sessions, and the AccountService
// with its accounts and roles, into mux. The ServiceRoot should link them
// (SessionService, AccountService, Links.Sessions).
func (s *Service) Register(mux *http.ServeMux) {
	gen.RegisterSessionService(mux, s)
	gen.RegisterSessionCollection(mux, s)
	gen.RegisterSession(mux, s)
	server.Handle(mux, http.MethodPost, SessionsURI, s.serveCreateSession)
	server.Handle(mux, http.MethodDelete, SessionsURI+"/{SessionId}", s.serveDeleteSession)

	gen.RegisterAccountService(mux, s)
	gen.RegisterManagerAccountCollection(mux, s)
	gen.RegisterManagerAccount(mux, s)
	gen.RegisterChangePasswordManagerAccount(mux, s)
	server.Handle(mux, http.MethodPost, AccountsURI, s.serveCreateAccount)
	server.Handle(mux, http.MethodDelete, AccountsURI+"/{ManagerAccountId}", s.serveDeleteAccount)
	gen.RegisterRoleCollection(mux, s)
	gen.RegisterRole(mux, s)
}

// --- SessionService ---

// GetSessionService implements gen.SessionServiceProvider.
func (s *Service) GetSessionService(context.Context) (*gen.SessionService, error) {
	return &gen.SessionService{
		Id:             "SessionService",
		Name:           "Session Service",
		ServiceEnabled: new(true),
		SessionTimeout: new(int64(s.accounts.Policy().SessionTimeout / time.Second)),
		Status:         &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		Sessions:       odata.Link(SessionsURI),
	}, nil
}

// PatchSessionService implements gen.SessionServiceProvider. SessionTimeout
// is writable.
func (s *Service) PatchSessionService(ctx context.Context, patch *gen.SessionServicePatch) (*gen.SessionService, error) {
	switch {
	case patch.ServiceEnabled != nil && !*patch.ServiceEnabled:
		return nil, server.NotWritable("ServiceEnabled")
	case patch.AbsoluteSessionTimeout != nil:
		return nil, server.NotWritable("AbsoluteSessionTimeout")
	case patch.AbsoluteSessionTimeoutEnabled != nil:
		return nil, server.NotWritable("AbsoluteSessionTimeoutEnabled")
	}
	if patch.SessionTimeout != nil {
		v := *patch.SessionTimeout
		if v < 0 || v > maxSetting {
			return nil, badProperty("SessionTimeout must be between 30 and 86400 seconds.")
		}
		if err := s.accounts.UpdatePolicy(func(p *Policy) { p.SessionTimeout = time.Duration(v) * time.Second }); err != nil {
			return nil, err
		}
	}
	return s.GetSessionService(ctx)
}

// ListSession implements gen.SessionCollectionProvider: all sessions for a
// principal with ConfigureManager, otherwise its own.
func (s *Service) ListSession(ctx context.Context) ([]string, error) {
	p, _ := PrincipalFrom(ctx)
	ids := []string{}
	for _, sess := range s.sessions.list(s.now(), s.accounts.Policy().SessionTimeout) {
		if sess.user == p.Account.UserName || p.Account.Has(ConfigureManager) {
			ids = append(ids, SessionsURI+"/"+sess.id)
		}
	}
	return ids, nil
}

// GetSession implements gen.SessionProvider. Handler lets only the owner and
// principals with ConfigureManager read a session.
func (s *Service) GetSession(_ context.Context, p server.Params) (*gen.Session, error) {
	sess, ok := s.sessions.get(p.Get("SessionId"), s.now(), s.accounts.Policy().SessionTimeout)
	if !ok {
		return nil, nil
	}
	return s.sessionResource(sess), nil
}

func (s *Service) sessionResource(sess session) *gen.Session {
	res := &gen.Session{
		ODataID:     SessionsURI + "/" + sess.id,
		Id:          sess.id,
		Name:        "User Session",
		UserName:    sess.user,
		SessionType: gen.SessionTypesRedfish,
		CreatedTime: sess.created.UTC().Format(time.RFC3339),
	}
	res.ClientOriginIPAddress = sess.origin
	if acc, ok := s.accounts.Get(sess.user); ok {
		res.Roles = []string{acc.RoleID}
	}
	return res
}

// serveCreateSession logs in: it creates a session for the UserName and
// Password of the body, and answers 201 with the session and its token in
// X-Auth-Token.
func (s *Service) serveCreateSession(w http.ResponseWriter, r *http.Request, _ server.Params) {
	var c gen.SessionCreate
	if !readBody(w, r, c.UnmarshalJSON) {
		return
	}
	if c.UserName == nil || c.Password == nil {
		server.WriteError(w, http.StatusBadRequest, odata.MsgPropertyMissing,
			"The properties UserName and Password are required to create a session.")
		return
	}
	acc, err := s.accounts.Authenticate(*c.UserName, *c.Password)
	if err != nil {
		s.loginFailed(w, err)
		return
	}
	limit := s.MaxSessions
	if limit == 0 {
		limit = DefaultMaxSessions
	}
	origin, _, _ := net.SplitHostPort(r.RemoteAddr)
	sess, token, ok := s.sessions.create(acc, origin, s.now(), s.accounts.Policy().SessionTimeout, limit, s.current)
	if !ok {
		server.WriteError(w, http.StatusServiceUnavailable, odata.MsgCreateLimitReachedForResource,
			"The create operation failed because the resource has reached the limit of possible resources.")
		return
	}
	w.Header().Set("X-Auth-Token", token)
	w.Header().Set("Cache-Control", "no-store")
	server.WriteCreated(w, SessionsURI+"/"+sess.id, s.sessionResource(sess))
}

// serveDeleteSession logs out: it ends the session.
func (s *Service) serveDeleteSession(w http.ResponseWriter, r *http.Request, p server.Params) {
	if !s.sessions.remove(p.Get("SessionId")) {
		server.NotFound(w, r)
		return
	}
	server.NoContent(w)
}

// --- AccountService ---

// GetAccountService implements gen.AccountServiceProvider.
func (s *Service) GetAccountService(_ context.Context, p server.Params) (*gen.AccountService, error) {
	if p.URI != AccountServiceURI {
		return nil, nil
	}
	pol := s.accounts.Policy()
	return &gen.AccountService{
		Id:                              "AccountService",
		Name:                            "Account Service",
		ServiceEnabled:                  new(true),
		Status:                          &gen.ResourceStatus{State: gen.ResourceStateEnabled, Health: gen.ResourceHealthOK},
		MinPasswordLength:               new(int64(pol.MinPasswordLength)),
		MaxPasswordLength:               new(int64(pol.MaxPasswordLength)),
		AccountLockoutThreshold:         new(int64(pol.LockoutThreshold)),
		AccountLockoutDuration:          new(int64(pol.LockoutDuration / time.Second)),
		AccountLockoutCounterResetAfter: new(int64(pol.LockoutCounterResetAfter / time.Second)),
		LocalAccountAuth:                gen.AccountServiceLocalAccountAuthEnabled,
		HTTPBasicAuth:                   gen.AccountServiceBasicAuthState(pol.BasicAuth),
		Accounts:                        odata.Link(AccountsURI),
		Roles:                           odata.Link(RolesURI),
	}, nil
}

// PatchAccountService implements gen.AccountServiceProvider. The password
// lengths, the lockout settings and HTTPBasicAuth are writable.
func (s *Service) PatchAccountService(ctx context.Context, p server.Params, patch *gen.AccountServicePatch) (*gen.AccountService, error) {
	if p.URI != AccountServiceURI {
		return nil, nil
	}
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"ServiceEnabled", patch.ServiceEnabled != nil && !*patch.ServiceEnabled},
		{"AuthFailureLoggingThreshold", patch.AuthFailureLoggingThreshold != nil},
		{"LocalAccountAuth", patch.LocalAccountAuth != nil && *patch.LocalAccountAuth != gen.AccountServiceLocalAccountAuthEnabled},
		{"AccountLockoutCounterResetEnabled", patch.AccountLockoutCounterResetEnabled != nil},
		{"PasswordExpirationDays", patch.PasswordExpirationDays != nil},
		{"RequireChangePasswordAction", patch.RequireChangePasswordAction != nil},
		{"EnforcePasswordHistoryCount", patch.EnforcePasswordHistoryCount != nil},
	} {
		if f.set {
			return nil, server.NotWritable(f.name)
		}
	}
	for _, v := range []*int64{patch.MinPasswordLength, patch.MaxPasswordLength, patch.AccountLockoutThreshold,
		patch.AccountLockoutDuration, patch.AccountLockoutCounterResetAfter} {
		if v != nil && (*v < 0 || *v > maxSetting) {
			return nil, badProperty("The account service settings must be between 0 and 2^30.")
		}
	}
	setInt := func(dst *int, v *int64) {
		if v != nil {
			*dst = int(*v)
		}
	}
	setDuration := func(dst *time.Duration, v *int64) {
		if v != nil {
			*dst = time.Duration(*v) * time.Second
		}
	}
	// Applied to the current policy in one step, so that a concurrent PATCH
	// of the SessionService is not undone.
	err := s.accounts.UpdatePolicy(func(pol *Policy) {
		setInt(&pol.MinPasswordLength, patch.MinPasswordLength)
		setInt(&pol.MaxPasswordLength, patch.MaxPasswordLength)
		setInt(&pol.LockoutThreshold, patch.AccountLockoutThreshold)
		setDuration(&pol.LockoutDuration, patch.AccountLockoutDuration)
		setDuration(&pol.LockoutCounterResetAfter, patch.AccountLockoutCounterResetAfter)
		if patch.HTTPBasicAuth != nil {
			pol.BasicAuth = BasicAuth(*patch.HTTPBasicAuth)
		}
	})
	if err != nil {
		return nil, err
	}
	return s.GetAccountService(ctx, p)
}

// ListManagerAccount implements gen.ManagerAccountCollectionProvider.
func (s *Service) ListManagerAccount(_ context.Context, p server.Params) ([]string, error) {
	if p.URI != AccountsURI {
		return nil, notFound(p)
	}
	ids := []string{}
	for _, acc := range s.accounts.List() {
		ids = append(ids, AccountsURI+"/"+acc.UserName)
	}
	return ids, nil
}

// GetManagerAccount implements gen.ManagerAccountProvider.
func (s *Service) GetManagerAccount(_ context.Context, p server.Params) (*gen.ManagerAccount, error) {
	if !strings.HasPrefix(p.URI, AccountsURI+"/") {
		return nil, nil
	}
	acc, ok := s.accounts.Get(p.Get("ManagerAccountId"))
	if !ok {
		return nil, nil
	}
	return s.accountResource(acc), nil
}

func (s *Service) accountResource(acc Account) *gen.ManagerAccount {
	uri := AccountsURI + "/" + acc.UserName
	return &gen.ManagerAccount{
		ODataID:                uri,
		Id:                     acc.UserName,
		Name:                   "User Account",
		UserName:               acc.UserName,
		RoleId:                 acc.RoleID,
		Enabled:                new(acc.Enabled),
		Locked:                 new(s.accounts.Locked(acc.UserName)),
		PasswordChangeRequired: new(false),
		AccountTypes:           []gen.ManagerAccountAccountTypes{gen.ManagerAccountAccountTypesRedfish},
		Links:                  &gen.ManagerAccountLinks{Role: odata.Link(RolesURI + "/" + acc.RoleID)},
		Actions: &gen.ManagerAccountActions{
			ChangePassword: &odata.ActionTarget{Target: uri + "/Actions/ManagerAccount.ChangePassword"},
		},
	}
}

// PatchManagerAccount implements gen.ManagerAccountProvider. Password,
// RoleId and Enabled are writable, and Locked can be set to false. A
// principal that changes its own account without ConfigureUsers may only
// change its password. A change is refused with 409 when the principal's own
// account got another password or role, or was disabled or deleted, after
// the request was authenticated.
func (s *Service) PatchManagerAccount(ctx context.Context, p server.Params, patch *gen.ManagerAccountPatch) (*gen.ManagerAccount, error) {
	if !strings.HasPrefix(p.URI, AccountsURI+"/") {
		return nil, nil
	}
	name := p.Get("ManagerAccountId")
	if _, ok := s.accounts.Get(name); !ok {
		return nil, nil
	}
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"UserName", patch.UserName != nil && *patch.UserName != name},
		{"Locked", patch.Locked != nil && *patch.Locked},
		{"PasswordChangeRequired", patch.PasswordChangeRequired != nil && *patch.PasswordChangeRequired},
		{"PasswordExpiration", patch.PasswordExpiration != nil},
		{"StrictAccountTypes", patch.StrictAccountTypes != nil},
		{"AccountExpiration", patch.AccountExpiration != nil},
		{"EmailAddress", patch.EmailAddress != nil},
		{"PhoneNumber", patch.PhoneNumber != nil},
		{"OneTimePasscodeDeliveryAddress", patch.OneTimePasscodeDeliveryAddress != nil},
	} {
		if f.set {
			return nil, server.NotWritable(f.name)
		}
	}
	pr, _ := PrincipalFrom(ctx)
	if !pr.Account.Has(ConfigureUsers) && (patch.RoleId != nil || patch.Enabled != nil || patch.Locked != nil) {
		return nil, errInsufficient
	}
	if patch.RoleId != nil || patch.Enabled != nil || patch.Password != nil {
		if patch.Password != nil {
			defer s.holdOwnSession(name, pr)()
		}
		prev, acc, err := s.accounts.change(name, patch.RoleId, patch.Enabled, patch.Password, pr.by())
		if err != nil {
			return nil, err
		}
		if patch.Password != nil {
			s.keepOwnSession(prev, acc, pr)
		}
	}
	if patch.Locked != nil {
		if err := s.accounts.unlock(name, pr.by()); err != nil {
			return nil, err
		}
	}
	return s.GetManagerAccount(ctx, p)
}

// keepOwnSession keeps the session of pr open when pr gave its own account a
// new password, turning it from generation prev into that of acc, the
// account as changed. The account's other sessions belong to an earlier
// generation and have ended. Nothing is kept when the account changed in
// between, by another password or by being disabled: then the session that
// authenticated pr has ended too. (change refuses such a change when given
// the principal, so this only guards callers that do not give it.)
func (s *Service) keepOwnSession(prev uint64, acc Account, pr Principal) {
	s.accounts.probe.rebind()
	if acc.Enabled && acc.UserName == pr.Account.UserName && pr.SessionID != "" && prev == pr.Account.gen {
		s.sessions.rebind(pr.SessionID, prev, acc.gen)
	}
}

// by returns the account that p was authenticated as, for change to check
// that it still holds, or nil for no principal.
func (p Principal) by() *Account {
	if p.Account.UserName == "" {
		return nil
	}
	return &p.Account
}

// holdOwnSession keeps the session of pr from being dropped as ended while pr
// changes the password of the account named name, if that is its own,
// until the returned function is called: from the change until
// keepOwnSession, the session has the account's old generation.
func (s *Service) holdOwnSession(name string, pr Principal) func() {
	if pr.SessionID == "" || name != pr.Account.UserName {
		return func() {}
	}
	return s.sessions.hold(pr.SessionID)
}

// ChangePasswordManagerAccount implements
// gen.ChangePasswordManagerAccountProvider: SessionAccountPassword must be the
// password of the principal's own account, as it was when the request was
// authenticated; 409 if the account changed since.
func (s *Service) ChangePasswordManagerAccount(ctx context.Context, p server.Params, params *gen.ManagerAccountChangePasswordParameters) (*server.ActionResult, error) {
	name := p.Get("ManagerAccountId")
	if _, ok := s.accounts.Get(name); !ok || !strings.HasPrefix(p.URI, AccountsURI+"/") {
		return nil, notFound(p)
	}
	if params.NewPassword == nil || params.SessionAccountPassword == nil {
		return nil, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterMissing,
			Message:   "The action ManagerAccount.ChangePassword requires the parameters NewPassword and SessionAccountPassword.",
		}
	}
	pr, _ := PrincipalFrom(ctx)
	// The password must be that of the account as the request was
	// authenticated, not one set since.
	if acc, err := s.accounts.Authenticate(pr.Account.UserName, *params.SessionAccountPassword); errors.Is(err, ErrBusy) {
		return nil, err
	} else if err != nil {
		return nil, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgActionParameterValueFormatError,
			Message:   "The value for the parameter SessionAccountPassword is not the password of the account of the session.",
		}
	} else if acc.gen != pr.Account.gen {
		return nil, errSuperseded
	}
	defer s.holdOwnSession(name, pr)()
	prev, acc, err := s.accounts.change(name, nil, nil, params.NewPassword, pr.by())
	if err != nil {
		return nil, err
	}
	s.keepOwnSession(prev, acc, pr)
	return nil, nil
}

// serveCreateAccount creates an account from UserName, Password and RoleId,
// and Enabled if given; 409 if the principal's account changed since the
// request was authenticated, as for PatchManagerAccount.
func (s *Service) serveCreateAccount(w http.ResponseWriter, r *http.Request, _ server.Params) {
	var c gen.ManagerAccountCreate
	if !readBody(w, r, c.UnmarshalJSON) {
		return
	}
	if c.UserName == nil || c.Password == nil || c.RoleId == nil {
		server.WriteError(w, http.StatusBadRequest, odata.MsgPropertyMissing,
			"The properties UserName, Password and RoleId are required to create an account.")
		return
	}
	switch {
	case c.Locked != nil && *c.Locked:
		server.WriteErr(w, server.NotWritable("Locked"))
		return
	case c.PasswordChangeRequired != nil && *c.PasswordChangeRequired:
		server.WriteErr(w, server.NotWritable("PasswordChangeRequired"))
		return
	}
	pr, _ := PrincipalFrom(r.Context())
	if err := s.accounts.create(*c.UserName, *c.Password, *c.RoleId, c.Enabled == nil || *c.Enabled, pr.by()); err != nil {
		server.WriteErr(w, err)
		return
	}
	acc, _ := s.accounts.Get(*c.UserName)
	server.WriteCreated(w, AccountsURI+"/"+acc.UserName, s.accountResource(acc))
}

// serveDeleteAccount deletes an account and ends its sessions; 409 if the
// principal's account changed since the request was authenticated.
func (s *Service) serveDeleteAccount(w http.ResponseWriter, r *http.Request, p server.Params) {
	name := p.Get("ManagerAccountId")
	pr, _ := PrincipalFrom(r.Context())
	if err := s.accounts.delete(name, pr.by()); err != nil {
		server.WriteErr(w, err)
		return
	}
	s.sessions.removeUser(name, "")
	server.NoContent(w)
}

// ListRole implements gen.RoleCollectionProvider.
func (s *Service) ListRole(_ context.Context, p server.Params) ([]string, error) {
	if p.URI != RolesURI {
		return nil, notFound(p)
	}
	ids := make([]string, len(roles))
	for i, r := range roles {
		ids[i] = RolesURI + "/" + r.id
	}
	return ids, nil
}

// GetRole implements gen.RoleProvider.
func (s *Service) GetRole(_ context.Context, p server.Params) (*gen.Role, error) {
	if !strings.HasPrefix(p.URI, RolesURI+"/") {
		return nil, nil
	}
	id := p.Get("RoleId")
	privs := RolePrivileges(id)
	if privs == nil {
		return nil, nil
	}
	assigned := make([]gen.PrivilegesPrivilegeType, len(privs))
	for i, pr := range privs {
		assigned[i] = gen.PrivilegesPrivilegeType(pr)
	}
	return &gen.Role{
		Id:                 id,
		Name:               id + " Role",
		RoleId:             id,
		IsPredefined:       new(true),
		AssignedPrivileges: assigned,
	}, nil
}

var errInsufficient = &server.Error{
	Status:    http.StatusForbidden,
	MessageID: odata.MsgInsufficientPrivilege,
	Message:   "There are insufficient privileges for the account or credentials associated with the current session to perform the requested operation.",
}

func notFound(p server.Params) error {
	return &server.Error{
		Status:    http.StatusNotFound,
		MessageID: odata.MsgResourceMissingAtURI,
		Message:   "The resource at URI '" + p.Path() + "' was not found.",
	}
}

// readBody parses the body of r with parse, or answers the error and returns
// false.
func readBody(w http.ResponseWriter, r *http.Request, parse func([]byte) error) bool {
	body, err := server.ReadBody(r)
	if err != nil {
		server.WriteErr(w, err)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 || parse(body) != nil {
		server.BadRequest(w, odata.MsgMalformedJSON, "The request body was not valid JSON.")
		return false
	}
	return true
}
