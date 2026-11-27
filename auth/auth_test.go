package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
	"strings"
	"sync"
	"testing"
	"time"
)

// testService serves the auth resources and a few others behind Handler, for
// accounts admin (Administrator), op (Operator) and viewer (ReadOnly), all
// with the password pw.
type testService struct {
	t     *testing.T
	s     *Service
	clock *clock
	h     http.Handler
}

func newTestService(t *testing.T) *testService {
	t.Helper()
	a, c := newAccounts(t, &Memory{})
	for _, acc := range []struct{ name, role string }{{"admin", Administrator}, {"op", Operator}, {"viewer", ReadOnly}} {
		if err := a.Create(acc.name, pw, acc.role); err != nil {
			t.Fatal(err)
		}
	}
	s := New(a)
	s.now = c.now
	mux := http.NewServeMux()
	s.Register(mux)
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	for _, p := range []string{"GET /redfish/v1/{$}", "GET /redfish/v1/$metadata", "GET /redfish/v1/Chassis",
		"POST /redfish/v1/Managers/hps/Actions/Manager.Reset", "POST /redfish/v1/UpdateService/upload"} {
		mux.HandleFunc(p, ok)
	}
	return &testService{t: t, s: s, clock: c, h: s.Handler(mux)}
}

// request is a request to send; auth is "user" for Basic with pw, "token:<t>"
// for a session, or "" for none.
func (ts *testService) do(method, path, auth, body string) (*httptest.ResponseRecorder, map[string]any) {
	ts.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	switch {
	case strings.HasPrefix(auth, "token:"):
		r.Header.Set("X-Auth-Token", strings.TrimPrefix(auth, "token:"))
	case auth != "":
		r.SetBasicAuth(auth, pw)
	}
	w := httptest.NewRecorder()
	ts.h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func (ts *testService) login(user, password string) (token, id string) {
	ts.t.Helper()
	w, m := ts.do("POST", SessionsURI, "", `{"UserName":"`+user+`","Password":"`+password+`"}`)
	if w.Code != http.StatusCreated || w.Header().Get("Cache-Control") != "no-store" {
		ts.t.Fatalf("login %s: status %d, Cache-Control %q, %s", user, w.Code, w.Header().Get("Cache-Control"), w.Body)
	}
	return w.Header().Get("X-Auth-Token"), m["Id"].(string)
}

func TestPublic(t *testing.T) {
	ts := newTestService(t)
	for _, p := range []string{"/redfish/v1/", "/redfish/v1/$metadata"} {
		if w, _ := ts.do("GET", p, "", ""); w.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", p, w.Code)
		}
	}
	w, m := ts.do("GET", "/redfish/v1/Chassis", "", "")
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Errorf("GET without credentials: status %d, WWW-Authenticate %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	if e, _ := m["error"].(map[string]any); e["code"] != "Base.1.0.NoValidSession" {
		t.Errorf("error = %v", m)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "admin", ""); w.Code != http.StatusOK {
		t.Errorf("GET with Basic: status %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/redfish/v1/Chassis", nil)
	r.SetBasicAuth("admin", "wrong password!")
	w = httptest.NewRecorder()
	ts.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET with a wrong password: status %d", w.Code)
	}
}

func TestSessions(t *testing.T) {
	ts := newTestService(t)
	token, id := ts.login("viewer", pw)
	if len(token) < 40 {
		t.Errorf("token %q is short", token)
	}
	w, m := ts.do("GET", SessionsURI+"/"+id, "token:"+token, "")
	if w.Code != http.StatusOK || m["UserName"] != "viewer" || m["Password"] != nil {
		t.Errorf("GET own session: status %d %v", w.Code, m)
	}
	// Failed logins.
	for _, body := range []string{`{"UserName":"viewer","Password":"nope nope nope"}`, `{"UserName":"viewer"}`, `nonsense`} {
		if w, _ := ts.do("POST", SessionsURI, "", body); w.Code != http.StatusUnauthorized && w.Code != http.StatusBadRequest {
			t.Errorf("login with %s: status %d", body, w.Code)
		}
	}
	// A viewer lists only its own sessions; an admin all.
	adminToken, _ := ts.login("admin", pw)
	_, m = ts.do("GET", SessionsURI, "token:"+token, "")
	if n := len(m["Members"].([]any)); n != 1 {
		t.Errorf("viewer sees %d sessions, want 1", n)
	}
	_, m = ts.do("GET", SessionsURI, "token:"+adminToken, "")
	if n := len(m["Members"].([]any)); n != 2 {
		t.Errorf("admin sees %d sessions, want 2", n)
	}
	if w, _ := ts.do("DELETE", SessionsURI+"/"+id, "op", ""); w.Code != http.StatusForbidden {
		t.Errorf("op ends viewer's session: status %d", w.Code)
	}
	// Logout.
	if w, _ := ts.do("DELETE", SessionsURI+"/"+id, "token:"+token, ""); w.Code != http.StatusNoContent {
		t.Errorf("logout: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("token after logout: status %d", w.Code)
	}
	// Timeout, counted from the last use.
	ts.clock.advance(20 * time.Minute)
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+adminToken, ""); w.Code != http.StatusOK {
		t.Errorf("token after 20 min: status %d", w.Code)
	}
	ts.clock.advance(29 * time.Minute)
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+adminToken, ""); w.Code != http.StatusOK {
		t.Errorf("token 29 min after its last use: status %d", w.Code)
	}
	ts.clock.advance(31 * time.Minute)
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+adminToken, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("token 31 min after its last use: status %d", w.Code)
	}
}

func TestSessionLimit(t *testing.T) {
	ts := newTestService(t)
	ts.s.MaxSessions = 2 // one per account
	first, _ := ts.login("viewer", pw)
	ts.login("op", pw)
	if w, _ := ts.do("POST", SessionsURI, "", `{"UserName":"admin","Password":"`+pw+`"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("third session: status %d, want 503", w.Code)
	}
	// Another session of viewer ends its oldest.
	second, _ := ts.login("viewer", pw)
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+first, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("viewer's oldest session: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+second, ""); w.Code != http.StatusOK {
		t.Errorf("viewer's new session: status %d", w.Code)
	}
}

func TestPrivileges(t *testing.T) {
	ts := newTestService(t)
	tests := []struct {
		method, path, user string
		want               int
	}{
		{"GET", "/redfish/v1/Chassis", "viewer", 200},
		{"POST", "/redfish/v1/UpdateService/upload", "viewer", 403},
		{"POST", "/redfish/v1/UpdateService/upload", "op", 200},
		{"POST", "/redfish/v1/Managers/hps/Actions/Manager.Reset", "op", 403},
		{"POST", "/redfish/v1/Managers/hps/Actions/Manager.Reset", "admin", 200},
		{"GET", AccountsURI, "viewer", 200},
		{"GET", AccountsURI + "/viewer", "viewer", 200},
		{"GET", AccountsURI + "/admin", "viewer", 403},
		{"GET", AccountsURI + "/viewer", "admin", 200},
		{"GET", RolesURI + "/Operator", "viewer", 200},
		{"PATCH", AccountServiceURI, "op", 403},
		{"PATCH", SessionServiceURI, "op", 403},
		{"POST", AccountsURI, "op", 403},
		{"DELETE", AccountsURI + "/viewer", "viewer", 403},
		{"DELETE", "/redfish/v1/TaskService/Tasks/1", "op", 403},
	}
	for _, tt := range tests {
		body := ""
		if tt.method != "GET" {
			body = `{}`
		}
		if w, _ := ts.do(tt.method, tt.path, tt.user, body); w.Code != tt.want {
			t.Errorf("%s %s as %s: status %d, want %d", tt.method, tt.path, tt.user, w.Code, tt.want)
		}
	}
}

func TestOwnAccount(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("viewer", pw)
	other, _ := ts.login("viewer", pw)
	// A viewer changes its own password, but not its role, nor another's
	// password.
	if w, _ := ts.do("PATCH", AccountsURI+"/viewer", "token:"+token, `{"RoleId":"Administrator"}`); w.Code != http.StatusForbidden {
		t.Errorf("viewer promotes itself: status %d", w.Code)
	}
	if w, _ := ts.do("PATCH", AccountsURI+"/op", "token:"+token, `{"Password":"a new long password"}`); w.Code != http.StatusForbidden {
		t.Errorf("viewer sets op's password: status %d", w.Code)
	}
	if w, m := ts.do("PATCH", AccountsURI+"/viewer", "token:"+token, `{"Password":"a new long password"}`); w.Code != http.StatusOK || m["Password"] != nil {
		t.Errorf("viewer sets its password: status %d %v", w.Code, m)
	}
	// The session that changed it stays, the other one ends.
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusOK {
		t.Errorf("own session after the change: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+other, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("other session after the change: status %d", w.Code)
	}
	if _, err := ts.s.accounts.Authenticate("viewer", "a new long password"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("op", pw)
	target := AccountsURI + "/op/Actions/ManagerAccount.ChangePassword"
	if w, _ := ts.do("POST", target, "token:"+token, `{"NewPassword":"a new long password","SessionAccountPassword":"not it at all"}`); w.Code != http.StatusBadRequest {
		t.Errorf("wrong SessionAccountPassword: status %d", w.Code)
	}
	if w, _ := ts.do("POST", target, "token:"+token, `{"NewPassword":"a new long password","SessionAccountPassword":"`+pw+`"}`); w.Code != http.StatusNoContent {
		t.Errorf("ChangePassword: status %d %s", w.Code, w.Body)
	}
	if _, err := ts.s.accounts.Authenticate("op", "a new long password"); err != nil {
		t.Errorf("new password: %v", err)
	}
	if w, _ := ts.do("POST", AccountsURI+"/viewer/Actions/ManagerAccount.ChangePassword", "token:"+token,
		`{"NewPassword":"a new long password","SessionAccountPassword":"a new long password"}`); w.Code != http.StatusForbidden {
		t.Errorf("op changes viewer's password: status %d", w.Code)
	}
}

func TestManageAccounts(t *testing.T) {
	ts := newTestService(t)
	w, m := ts.do("POST", AccountsURI, "admin", `{"UserName":"bmc","Password":"`+pw+`","RoleId":"Operator"}`)
	if w.Code != http.StatusCreated || w.Header().Get("Location") != AccountsURI+"/bmc" || m["RoleId"] != "Operator" {
		t.Fatalf("create: status %d %v", w.Code, m)
	}
	for _, tt := range []struct {
		body string
		want int
	}{
		{`{"UserName":"bmc","Password":"` + pw + `","RoleId":"Operator"}`, http.StatusConflict},
		{`{"UserName":"x","Password":"short","RoleId":"Operator"}`, http.StatusBadRequest},
		{`{"UserName":"x","Password":"` + pw + `","RoleId":"God"}`, http.StatusBadRequest},
		{`{"UserName":"x","Password":"` + pw + `"}`, http.StatusBadRequest},
	} {
		if w, _ := ts.do("POST", AccountsURI, "admin", tt.body); w.Code != tt.want {
			t.Errorf("create %s: status %d, want %d", tt.body, w.Code, tt.want)
		}
	}
	token, _ := ts.login("bmc", pw)
	// Disabling an account ends its sessions.
	if w, _ := ts.do("PATCH", AccountsURI+"/bmc", "admin", `{"Enabled":false}`); w.Code != http.StatusOK {
		t.Errorf("disable: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("session of a disabled account: status %d", w.Code)
	}
	if w, _ := ts.do("PATCH", AccountsURI+"/bmc", "admin", `{"UserName":"renamed"}`); w.Code != http.StatusBadRequest {
		t.Errorf("rename: status %d", w.Code)
	}
	if w, _ := ts.do("DELETE", AccountsURI+"/bmc", "admin", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: status %d", w.Code)
	}
	if w, _ := ts.do("DELETE", AccountsURI+"/admin", "admin", ""); w.Code != http.StatusBadRequest {
		t.Errorf("delete the last admin: status %d", w.Code)
	}
	_, m = ts.do("GET", AccountsURI+"/admin", "admin", "")
	if m["Password"] != nil || m["Locked"] != false || m["Links"].(map[string]any)["Role"].(map[string]any)["@odata.id"] != RolesURI+"/Administrator" {
		t.Errorf("account = %v", m)
	}
}

func TestAccountService(t *testing.T) {
	ts := newTestService(t)
	if w, m := ts.do("PATCH", AccountServiceURI, "admin", `{"MinPasswordLength":20,"HTTPBasicAuth":"Disabled"}`); w.Code != http.StatusOK || m["MinPasswordLength"] != 20.0 {
		t.Fatalf("PATCH: status %d %v", w.Code, m)
	}
	// Basic is off now: sessions still work.
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "admin", ""); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("Basic after disabling it: status %d, WWW-Authenticate %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	token, _ := ts.login("admin", pw)
	if w, _ := ts.do("PATCH", AccountServiceURI, "token:"+token, `{"MinPasswordLength":0}`); w.Code != http.StatusBadRequest {
		t.Errorf("MinPasswordLength 0: status %d", w.Code)
	}
	if w, _ := ts.do("PATCH", SessionServiceURI, "token:"+token, `{"SessionTimeout":10}`); w.Code != http.StatusBadRequest {
		t.Errorf("SessionTimeout 10: status %d", w.Code)
	}
	if w, m := ts.do("PATCH", SessionServiceURI, "token:"+token, `{"SessionTimeout":60}`); w.Code != http.StatusOK || m["SessionTimeout"] != 60.0 {
		t.Errorf("SessionTimeout 60: status %d %v", w.Code, m)
	}
}

// TestClientCertificate authenticates by the Common Name of a verified TLS
// client certificate.
func TestClientCertificate(t *testing.T) {
	ts := newTestService(t)
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "op"}}
	for _, tt := range []struct {
		name  string
		state tls.ConnectionState
		path  string
		want  int
	}{
		{"verified", tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}, "/redfish/v1/UpdateService/upload", 200},
		{"verified, insufficient", tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}, "/redfish/v1/Managers/hps/Actions/Manager.Reset", 403},
		{"not verified", tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}, "/redfish/v1/UpdateService/upload", 401},
		{"unknown account", tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: "nobody"}}}}}, "/redfish/v1/UpdateService/upload", 401},
	} {
		r := httptest.NewRequest("POST", tt.path, strings.NewReader("{}"))
		r.TLS = &tt.state
		w := httptest.NewRecorder()
		ts.h.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, w.Code, tt.want)
		}
	}
}

func TestRoles(t *testing.T) {
	ts := newTestService(t)
	_, m := ts.do("GET", RolesURI, "viewer", "")
	if n := len(m["Members"].([]any)); n != 3 {
		t.Errorf("%d roles", n)
	}
	_, m = ts.do("GET", RolesURI+"/ReadOnly", "viewer", "")
	if privs, _ := m["AssignedPrivileges"].([]any); len(privs) != 2 || m["IsPredefined"] != true {
		t.Errorf("ReadOnly = %v", m)
	}
	if w, _ := ts.do("GET", RolesURI+"/Nobody", "viewer", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown role: status %d", w.Code)
	}
}

// TestLockout locks an account by failed logins: logins with the password
// fail, its open session and its certificate keep working.
func TestLockout(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("op", pw)
	for range DefaultPolicy.LockoutThreshold {
		ts.do("POST", SessionsURI, "", `{"UserName":"op","Password":"guess guess guess"}`)
	}
	if w, _ := ts.do("POST", SessionsURI, "", `{"UserName":"op","Password":"`+pw+`"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("login of a locked account: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "op", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("Basic of a locked account: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusOK {
		t.Errorf("open session of a locked account: status %d", w.Code)
	}
	_, m := ts.do("GET", AccountsURI+"/op", "admin", "")
	if m["Locked"] != true {
		t.Errorf("Locked = %v", m["Locked"])
	}
	if w, _ := ts.do("PATCH", AccountsURI+"/op", "admin", `{"Locked":false}`); w.Code != http.StatusOK {
		t.Errorf("unlock: status %d", w.Code)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "op", ""); w.Code != http.StatusOK {
		t.Errorf("Basic after unlock: status %d", w.Code)
	}
}

func TestSelfPatchLimits(t *testing.T) {
	ts := newTestService(t)
	for _, body := range []string{`{"Enabled":false}`, `{"Locked":false}`, `{"RoleId":"ReadOnly"}`} {
		if w, _ := ts.do("PATCH", AccountsURI+"/viewer", "viewer", body); w.Code != http.StatusForbidden {
			t.Errorf("viewer PATCH %s: status %d, want 403", body, w.Code)
		}
	}
	if w, _ := ts.do("PATCH", AccountsURI+"/viewer", "admin", `{"Locked":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("Locked true: status %d, want 400", w.Code)
	}
	w, _ := ts.do("PATCH", AccountsURI+"/op", "admin", `{"RoleId":"ReadOnly","Password":"short"}`)
	if _, m := ts.do("GET", AccountsURI+"/op", "admin", ""); w.Code != http.StatusBadRequest || m["RoleId"] != Operator {
		t.Errorf("PATCH with a bad password: status %d, role %v", w.Code, m["RoleId"])
	}
}

// TestSessionsEndWithTheAccount checks that sessions end when their account
// is deleted, even when one of that name is created again, or gets another
// password through the Accounts API.
func TestSessionsEndWithTheAccount(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("op", pw)
	if w, _ := ts.do("DELETE", AccountsURI+"/op", "admin", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", w.Code)
	}
	ts.s.accounts.Create("op", pw, Administrator)
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("session of a deleted account that was created again: status %d", w.Code)
	}
	token, _ = ts.login("viewer", pw)
	ts.s.accounts.SetPassword("viewer", "set through the API")
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("session after a password change through the API: status %d", w.Code)
	}
	// A role change keeps the session, with the new role.
	token, _ = ts.login("op", pw)
	ro := ReadOnly
	ts.s.accounts.Update("op", &ro, nil)
	if w, _ := ts.do("POST", "/redfish/v1/UpdateService/upload", "token:"+token, "{}"); w.Code != http.StatusForbidden {
		t.Errorf("session after a demotion: status %d, want 403", w.Code)
	}
}

func TestDisabledCertificate(t *testing.T) {
	ts := newTestService(t)
	off := false
	ts.s.accounts.Update("op", nil, &off)
	r := httptest.NewRequest("GET", "/redfish/v1/Chassis", nil)
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: "op"}}}}}
	w := httptest.NewRecorder()
	ts.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("certificate of a disabled account: status %d", w.Code)
	}
}

func TestBusyLogin(t *testing.T) {
	ts := newTestService(t)
	defer func(d time.Duration) { verifyWait = d }(verifyWait)
	verifyWait = 10 * time.Millisecond
	for range verifySlots {
		ts.s.accounts.slots <- struct{}{}
	}
	w, _ := ts.do("POST", SessionsURI, "", `{"UserName":"viewer","Password":"`+pw+`"}`)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("login while busy: status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestRemoteAccountService(t *testing.T) {
	ts := newTestService(t)
	for _, p := range []string{"/redfish/v1/Managers/hps/RemoteAccountService", "/redfish/v1/Managers/hps/RemoteAccountService/Accounts",
		"/redfish/v1/Managers/hps/RemoteAccountService/Accounts/admin", "/redfish/v1/Managers/hps/RemoteAccountService/Roles"} {
		if w, _ := ts.do("GET", p, "admin", ""); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, w.Code)
		}
	}
}

// TestCrossOrigin refuses a change a browser sends from another site with
// credentials it keeps.
func TestCrossOrigin(t *testing.T) {
	ts := newTestService(t)
	r := httptest.NewRequest("POST", "/redfish/v1/UpdateService/upload", strings.NewReader("{}"))
	r.SetBasicAuth("admin", pw)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	ts.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: status %d, want 403", w.Code)
	}
}

// TestPathsOverHTTP sends odd paths through a real server: without
// credentials none but the public ones are served.
func TestPathsOverHTTP(t *testing.T) {
	ts := newTestService(t)
	srv := httptest.NewServer(ts.h)
	t.Cleanup(srv.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{
		"//redfish/v1/Chassis", "/redfish/v1//Chassis", "/redfish/v1/./Chassis", "/redfish/v1/$metadata/../Chassis",
		"/redfish/v1/%2e%2e/v1/Chassis", "/redfish/v1/odata/../AccountService/Accounts", "/redfish/v1/Chassis/",
		"/redfish/v1/SessionService/Sessions/../../AccountService/Accounts", "/REDFISH/v1/Chassis", "/redfish/v1/%43hassis",
	} {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.URL.Opaque = p // send the path as written
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s without credentials: status 200", p)
		}
	}
}

// TestEvictOldest opens more sessions of one account than it may have: the
// oldest ends.
func TestEvictOldest(t *testing.T) {
	ts := newTestService(t)
	ts.s.MaxSessions = 8 // two per account
	var tokens []string
	for range 3 {
		tok, _ := ts.login("viewer", pw)
		tokens = append(tokens, tok)
		ts.clock.advance(time.Second)
	}
	for i, want := range []int{http.StatusUnauthorized, http.StatusOK, http.StatusOK} {
		if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+tokens[i], ""); w.Code != want {
			t.Errorf("session %d: status %d, want %d", i, w.Code, want)
		}
	}
}

// TestDisableEndsSessions disables an account and enables it again: its
// sessions stay ended.
func TestDisableEndsSessions(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("op", pw)
	off, on := false, true
	ts.s.accounts.Update("op", nil, &off)
	ts.s.accounts.Update("op", nil, &on)
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("session after disable and enable: status %d", w.Code)
	}
}

// TestCrossOriginAllowed accepts a change from a trusted origin.
func TestCrossOriginAllowed(t *testing.T) {
	ts := newTestService(t)
	ts.s.CrossOrigin = http.NewCrossOriginProtection()
	ts.s.CrossOrigin.AddTrustedOrigin("https://ui.example")
	mux := http.NewServeMux()
	ts.s.Register(mux)
	mux.HandleFunc("POST /redfish/v1/UpdateService/upload", func(w http.ResponseWriter, r *http.Request) {})
	h := ts.s.Handler(mux)
	for origin, want := range map[string]int{"https://ui.example": 200, "https://evil.example": 403} {
		r := httptest.NewRequest("POST", "/redfish/v1/UpdateService/upload", strings.NewReader("{}"))
		r.SetBasicAuth("admin", pw)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Origin %s: status %d, want %d", origin, w.Code, want)
		}
		if want == 403 && !strings.Contains(w.Body.String(), "InsufficientPrivilege") {
			t.Errorf("refusal body %q is not a Redfish error", w.Body)
		}
	}
}

// TestRebindAfterStaleRequest uses a session between a change of its own
// password and the rebind: the request is refused, but the session is not
// ended, so the rebind still keeps it.
func TestRebindAfterStaleRequest(t *testing.T) {
	ts := newTestService(t)
	token, id := ts.login("op", pw)
	old, _ := ts.s.accounts.Get("op")
	newPW := "a new long password"
	prev, acc, err := ts.s.accounts.change("op", nil, nil, &newPW, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("before the rebind: status %d", w.Code)
	}
	ts.s.keepOwnSession(prev, acc, Principal{Account: old, SessionID: id})
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusOK {
		t.Errorf("after the rebind: status %d", w.Code)
	}
}

// TestRevokedSessionStaysRevoked changes the password of an account while
// one of its own requests, authenticated before, changes it again: the
// session of that request is not kept.
func TestRevokedSessionStaysRevoked(t *testing.T) {
	ts := newTestService(t)
	token, id := ts.login("op", pw)
	authenticated, _ := ts.s.accounts.Get("op")
	ts.s.accounts.SetPassword("op", "reset by an administrator") // revokes the session
	newPW := "a new long password"
	prev, acc, err := ts.s.accounts.change("op", nil, nil, &newPW, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts.s.keepOwnSession(prev, acc, Principal{Account: authenticated, SessionID: id})
	if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked session: status %d", w.Code)
	}
}

// TestStaleSessionsFreeRoom fills the sessions, then ends one by changing the
// password of its account: a new login gets its place.
func TestStaleSessionsFreeRoom(t *testing.T) {
	ts := newTestService(t)
	ts.s.MaxSessions = 4 // one per account
	ts.s.accounts.Create("u4", pw, ReadOnly)
	ts.s.accounts.Create("u5", pw, ReadOnly)
	for _, u := range []string{"admin", "op", "viewer", "u4"} {
		ts.login(u, pw)
	}
	if w, _ := ts.do("POST", SessionsURI, "", `{"UserName":"u5","Password":"`+pw+`"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("fifth session: status %d, want 503", w.Code)
	}
	ts.s.accounts.SetPassword("viewer", "another long password")
	ts.login("u5", pw)
}

// TestPolicyPatchesDoNotUndoEachOther patches the AccountService and the
// SessionService at once: each saved policy has the latest SessionTimeout,
// which only grows, and once set, the HTTPBasicAuth of the other PATCH.
func TestPolicyPatchesDoNotUndoEachOther(t *testing.T) {
	ts := newTestService(t)
	var mu sync.Mutex
	var bad []string
	lastTimeout, unadvertised := 0, false
	ts.s.accounts.storage = savingFunc(func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		var timeout int
		var basic string
		for _, line := range strings.Split(string(data), "\n") {
			fmt.Sscanf(line, "policy SessionTimeout %d", &timeout)
			fmt.Sscanf(line, "policy HTTPBasicAuth %s", &basic)
		}
		if timeout < lastTimeout || unadvertised && basic != "Unadvertised" {
			bad = append(bad, fmt.Sprintf("timeout %d after %d, basic %s", timeout, lastTimeout, basic))
		}
		lastTimeout, unadvertised = timeout, unadvertised || basic == "Unadvertised"
	})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 2000 {
			ts.s.PatchSessionService(context.Background(), &gen.SessionServicePatch{SessionTimeout: ptr(int64(2000 + i))})
		}
	})
	wg.Go(func() {
		for i := range 2000 {
			basic := gen.AccountServiceBasicAuthStateEnabled
			if i == 1000 {
				basic = gen.AccountServiceBasicAuthStateUnadvertised
			} else if i > 1000 {
				basic = ""
			}
			patch := &gen.AccountServicePatch{MinPasswordLength: ptr(int64(12))}
			if basic != "" {
				patch.HTTPBasicAuth = &basic
			}
			ts.s.PatchAccountService(context.Background(), server.Params{URI: AccountServiceURI}, patch)
		}
	})
	wg.Wait()
	if len(bad) > 0 {
		t.Errorf("lost updates: %v", bad[:min(len(bad), 3)])
	}
}

func ptr[T any](v T) *T { return &v }

// savingFunc is a Storage that hands every saved database to a function.
type savingFunc func([]byte)

func (f savingFunc) Load() ([]byte, error)  { return nil, nil }
func (f savingFunc) Save(data []byte) error { f(data); return nil }

func TestBusyPasswordChange(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("op", pw)
	defer func(d time.Duration) { verifyWait = d }(verifyWait)
	verifyWait = 10 * time.Millisecond
	release := fillSlots(ts.s.accounts)
	defer release()
	if w, _ := ts.do("PATCH", AccountsURI+"/op", "token:"+token, `{"Password":"a new long password"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("password change while busy: status %d, want 503", w.Code)
	}
}

// TestPolicyPatchBounds refuses values whose conversion to a duration would
// wrap around to an accepted one.
func TestPolicyPatchBounds(t *testing.T) {
	ts := newTestService(t)
	big := int64(1)<<55 + 120 // * 1e9 wraps to exactly 120 s
	for _, tt := range []struct{ uri, body string }{
		{AccountServiceURI, fmt.Sprintf(`{"AccountLockoutDuration":%d}`, big)},
		{SessionServiceURI, fmt.Sprintf(`{"SessionTimeout":%d}`, big)},
	} {
		if w, _ := ts.do("PATCH", tt.uri, "admin", tt.body); w.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s %s: status %d, want 400", tt.uri, tt.body, w.Code)
		}
	}
}

// TestBusyChangePasswordAction answers 503 to ManagerAccount.ChangePassword
// while no slot is free to check the session's password.
func TestBusyChangePasswordAction(t *testing.T) {
	ts := newTestService(t)
	token, _ := ts.login("op", pw)
	defer func(d time.Duration) { verifyWait = d }(verifyWait)
	verifyWait = 10 * time.Millisecond
	release := fillSlots(ts.s.accounts)
	defer release()
	w, _ := ts.do("POST", AccountsURI+"/op/Actions/ManagerAccount.ChangePassword", "token:"+token,
		`{"NewPassword":"a new long password","SessionAccountPassword":"not the right one"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("ChangePassword while busy: status %d, want 503", w.Code)
	}
}

// TestOwnPasswordChangeKeepsSession logs in to a full session store while a
// session changes its own password, between the change and the rebind: the
// session is not dropped as ended.
func TestOwnPasswordChangeKeepsSession(t *testing.T) {
	ts := newTestService(t)
	ts.s.MaxSessions = 4
	ts.s.accounts.Create("u4", pw, ReadOnly)
	ts.s.accounts.Create("u5", pw, ReadOnly)
	token, _ := ts.login("op", pw)
	for _, u := range []string{"admin", "viewer", "u4"} {
		ts.login(u, pw)
	}
	// During the first change the store is full: u5 cannot log in, and
	// op's session is not dropped. During the second, u4 replaces its own
	// session.
	logins := []func(){
		func() {
			if w, m := ts.do("POST", SessionsURI, "", `{"UserName":"u5","Password":"`+pw+`"}`); w.Code != http.StatusServiceUnavailable || messageID(m) != odata.MsgCreateLimitReached {
				t.Errorf("login u5 with a full store: status %d, message %q", w.Code, messageID(m))
			}
		},
		func() {
			if w, _ := ts.do("POST", SessionsURI, "", `{"UserName":"u4","Password":"`+pw+`"}`); w.Code != http.StatusCreated {
				t.Errorf("login u4 with a full store: status %d, want 201", w.Code)
			}
		},
	}
	for i, req := range []struct{ method, uri, body string }{
		{"PATCH", AccountsURI + "/op", `{"Password":"a new long password"}`},
		{"POST", AccountsURI + "/op/Actions/ManagerAccount.ChangePassword", `{"NewPassword":"another long password","SessionAccountPassword":"a new long password"}`},
	} {
		ran := false
		ts.s.beforeRebind = func() {
			ts.s.beforeRebind = nil
			ran = true
			logins[i]()
		}
		if w, _ := ts.do(req.method, req.uri, "token:"+token, req.body); w.Code/100 != 2 {
			t.Fatalf("%s %s: status %d", req.method, req.uri, w.Code)
		}
		if !ran {
			t.Errorf("%s %s did not reach the rebind", req.method, req.uri)
		}
		if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusOK {
			t.Errorf("after %s %s: the session ended, status %d", req.method, req.uri, w.Code)
		}
	}
}

// messageID returns the MessageId of an error response.
func messageID(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	info, _ := e["@Message.ExtendedInfo"].([]any)
	if len(info) == 0 {
		return ""
	}
	first, _ := info[0].(map[string]any)
	id, _ := first["MessageId"].(string)
	return id
}

// TestOwnPasswordChangeAfterReset: op's password is reset while op's own
// change of it is being hashed. op's change is refused, the
// administrator's password stays, and op's session has ended. The same for
// the ChangePassword action, and for its password proof made after the
// reset with the new password, which is refused before the new password is
// hashed.
func TestOwnPasswordChangeAfterReset(t *testing.T) {
	ts := newTestService(t)
	reset := "set by the administrator"
	for _, req := range []struct{ method, uri, body string }{
		{"PATCH", AccountsURI + "/op", `{"Password":"a new long password"}`},
		{"POST", AccountsURI + "/op/Actions/ManagerAccount.ChangePassword", `{"NewPassword":"a new long password","SessionAccountPassword":"` + pw + `"}`},
	} {
		ts.s.accounts.SetPassword("op", pw)
		token, _ := ts.login("op", pw)
		// Not a PATCH of the account: PATCHes of accounts run one at a
		// time.
		ts.s.accounts.beforeChange = func() {
			ts.s.accounts.beforeChange = nil
			if err := ts.s.accounts.SetPassword("op", reset); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
		if w, _ := ts.do(req.method, req.uri, "token:"+token, req.body); w.Code != http.StatusConflict {
			t.Errorf("%s %s after a reset: status %d, want 409", req.method, req.uri, w.Code)
		}
		if _, err := ts.s.accounts.Authenticate("op", reset); err != nil {
			t.Errorf("%s %s: the reset password was replaced", req.method, req.uri)
		}
		if w, _ := ts.do("GET", "/redfish/v1/Chassis", "token:"+token, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: the session survived the reset, status %d", req.method, req.uri, w.Code)
		}
	}

	// A session authenticated before the reset proves the new password.
	ts.s.accounts.SetPassword("op", pw)
	acc, _ := ts.s.accounts.Get("op")
	ts.s.accounts.SetPassword("op", reset)
	mux := http.NewServeMux()
	ts.s.Register(mux)
	r := httptest.NewRequest("POST", AccountsURI+"/op/Actions/ManagerAccount.ChangePassword",
		strings.NewReader(`{"NewPassword":"a new long password","SessionAccountPassword":"`+reset+`"}`))
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, Principal{Account: acc}))
	w := httptest.NewRecorder()
	ts.s.accounts.beforeChange = func() { t.Error("the new password was hashed after a superseded proof") }
	mux.ServeHTTP(w, r)
	ts.s.accounts.beforeChange = nil
	if w.Code != http.StatusConflict {
		t.Errorf("proof with a password set after the request was authenticated: status %d, want 409", w.Code)
	}
	if _, err := ts.s.accounts.Authenticate("op", reset); err != nil {
		t.Error("the reset password was replaced")
	}
}
