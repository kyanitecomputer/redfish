package auth

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"src.kyanite.computer/redfish/server"
)

const pw = "correct horse battery"

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
func newClock() *clock     { return &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)} }
func status(err error) int { e, _ := errors.AsType[*server.Error](err); return statusOf(e) }
func statusOf(e *server.Error) int {
	if e == nil {
		return 0
	}
	return e.Status
}

func newAccounts(t *testing.T, s Storage) (*Accounts, *clock) {
	t.Helper()
	a, err := NewAccounts(s)
	if err != nil {
		t.Fatal(err)
	}
	a.Iterations = 1000
	c := newClock()
	a.now = c.now
	return a, c
}

func TestAccountsAuthenticate(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	if err := a.Create("admin", pw, Administrator); err != nil {
		t.Fatal(err)
	}
	if acc, err := a.Authenticate("admin", pw); err != nil || acc.RoleId != Administrator {
		t.Errorf("Authenticate = %v, %v", acc, err)
	}
	for _, tt := range []struct{ name, password string }{
		{"admin", "wrong password!"}, {"nobody", pw}, {"Admin", pw},
	} {
		if _, err := a.Authenticate(tt.name, tt.password); err != errLogin {
			t.Errorf("Authenticate(%q, %q) = %v, want errLogin", tt.name, tt.password, err)
		}
	}
	// A remembered password is forgotten when it changes.
	if err := a.Create("op", pw, Operator); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Fatal(err)
	}
	if err := a.SetPassword("admin", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate("admin", pw); err == nil {
		t.Error("the old password still works")
	}
	if _, err := a.Authenticate("admin", "another long password"); err != nil {
		t.Errorf("new password: %v", err)
	}
	off := false
	if err := a.Update("op", nil, &off); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate("op", pw); err == nil {
		t.Error("a disabled account authenticated")
	}
}

func TestAccountsLockout(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	p := a.Policy()
	p.LockoutThreshold, p.LockoutDuration, p.LockoutCounterResetAfter = 3, time.Minute, 30*time.Second
	if err := a.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	if err := a.Create("admin", pw, Administrator); err != nil {
		t.Fatal(err)
	}
	// Failures further apart than LockoutCounterResetAfter do not add up.
	for range 4 {
		a.Authenticate("admin", "wrong password!")
		c.advance(40 * time.Second)
	}
	if a.Locked("admin") {
		t.Fatal("locked by failures spread out")
	}
	for range 3 {
		a.Authenticate("admin", "wrong password!")
	}
	if !a.Locked("admin") {
		t.Fatal("not locked after 3 failures")
	}
	if _, err := a.Authenticate("admin", pw); err == nil {
		t.Error("a locked account authenticated")
	}
	c.advance(61 * time.Second)
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Errorf("after the lockout: %v", err)
	}
	for range 3 {
		a.Authenticate("admin", "wrong password!")
	}
	a.Unlock("admin")
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Errorf("after Unlock: %v", err)
	}
}

func TestAccountsRules(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	tests := []struct {
		name             string
		user, pass, role string
		wantStatus       int
	}{
		{"short password", "u1", "short", ReadOnly, http.StatusBadRequest},
		{"long password", "u1", strings.Repeat("x", 129), ReadOnly, http.StatusBadRequest},
		{"unknown role", "u1", pw, "Root", http.StatusBadRequest},
		{"bad name", "../x", pw, ReadOnly, http.StatusBadRequest},
		{"name with space", "a b", pw, ReadOnly, http.StatusBadRequest},
		{"leading dot", ".a", pw, ReadOnly, http.StatusBadRequest},
		{"too long name", strings.Repeat("a", 33), pw, ReadOnly, http.StatusBadRequest},
		{"admin", "admin", pw, Administrator, 0},
		{"duplicate", "admin", pw, ReadOnly, http.StatusConflict},
	}
	for _, tt := range tests {
		if got := status(a.Create(tt.user, tt.pass, tt.role)); got != tt.wantStatus {
			t.Errorf("%s: status %d, want %d", tt.name, got, tt.wantStatus)
		}
	}
	// The last enabled Administrator stays.
	off, ro := false, ReadOnly
	if got := status(a.Update("admin", nil, &off)); got != http.StatusBadRequest {
		t.Errorf("disabling the last admin: status %d", got)
	}
	if got := status(a.Update("admin", &ro, nil)); got != http.StatusBadRequest {
		t.Errorf("demoting the last admin: status %d", got)
	}
	if got := status(a.Delete("admin")); got != http.StatusBadRequest {
		t.Errorf("deleting the last admin: status %d", got)
	}
	if err := a.Create("admin2", pw, Administrator); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("admin"); err != nil {
		t.Errorf("deleting one of two admins: %v", err)
	}
	if got := status(a.Delete("admin")); got != http.StatusNotFound {
		t.Errorf("deleting a missing account: status %d", got)
	}
}

func TestAccountsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts")
	a, _ := newAccounts(t, File(path))
	if err := a.Create("admin", pw, Administrator); err != nil {
		t.Fatal(err)
	}
	if err := a.Create("viewer", pw, ReadOnly); err != nil {
		t.Fatal(err)
	}
	p := a.Policy()
	p.MinPasswordLength, p.BasicAuth, p.SessionTimeout = 16, BasicDisabled, time.Hour
	if err := a.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v; want mode 0600", fi, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), pw) {
		t.Fatal("the file holds a password")
	}

	b, _ := newAccounts(t, File(path))
	if got := b.List(); len(got) != 2 || got[0].UserName != "admin" || got[1].RoleId != ReadOnly {
		t.Errorf("accounts = %v", got)
	}
	if b.Policy() != p {
		t.Errorf("policy = %+v, want %+v", b.Policy(), p)
	}
	if _, err := b.Authenticate("viewer", pw); err != nil {
		t.Errorf("Authenticate after reload: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("files left: %v", entries)
	}
}

func TestAccountsBadDatabase(t *testing.T) {
	for _, data := range []string{
		"account admin Root enabled $pbkdf2-sha256$1$AA$AA\n",
		"account admin Administrator maybe $pbkdf2-sha256$1$AA$AA\n",
		"account a Administrator enabled x\naccount a ReadOnly enabled x\n",
		"policy SessionTimeout 5\n",
		"policy Unknown 5\n",
		"something else\n",
	} {
		m := &Memory{}
		m.Save([]byte(data))
		if _, err := NewAccounts(m); err == nil {
			t.Errorf("NewAccounts accepted %q", data)
		}
	}
}

// failingStorage fails every Save.
type failingStorage struct{ Memory }

func (f *failingStorage) Save([]byte) error { return errors.New("disk full") }

func TestAccountsSaveFails(t *testing.T) {
	a, _ := newAccounts(t, &failingStorage{})
	if err := a.Create("admin", pw, Administrator); err == nil {
		t.Fatal("Create succeeded without saving")
	}
	if len(a.List()) != 0 {
		t.Error("an account that was not saved exists")
	}
}

func TestVerify(t *testing.T) {
	h, err := hash(pw, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$pbkdf2-sha256$1000$") || !verify(h, pw) || verify(h, pw+"x") {
		t.Errorf("hash %q does not verify as it should", h)
	}
	for _, bad := range []string{"", "$pbkdf2-sha256$0$AA$AA", "$md5$1$AA$AA", "$pbkdf2-sha256$1$!!$AA"} {
		if verify(bad, pw) {
			t.Errorf("verify(%q) = true", bad)
		}
	}
}

// TestRememberedPasswordIsPerAccount checks that a remembered password
// lets in only its own account.
func TestRememberedPasswordIsPerAccount(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("alice", pw, Administrator)
	a.Create("carol", "carol's own password", ReadOnly)
	if _, err := a.Authenticate("alice", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate("carol", pw); err == nil {
		t.Error("carol logged in with alice's remembered password")
	}
}

// TestLockoutCounterResets checks that failures further apart than
// LockoutCounterResetAfter do not add up, and that failures while locked
// do not make the lockout longer.
func TestLockoutCounterResets(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	p := a.Policy()
	p.LockoutThreshold, p.LockoutDuration, p.LockoutCounterResetAfter = 3, time.Minute, 30*time.Second
	a.SetPolicy(p)
	a.Create("admin", pw, Administrator)
	a.Authenticate("admin", "wrong password!")
	a.Authenticate("admin", "wrong password!")
	c.advance(31 * time.Second)
	a.Authenticate("admin", "wrong password!")
	a.Authenticate("admin", "wrong password!")
	if a.Locked("admin") {
		t.Fatal("locked by 2+2 failures 31 s apart")
	}
	a.Authenticate("admin", "wrong password!")
	if !a.Locked("admin") {
		t.Fatal("not locked by 3 failures within 30 s")
	}
	c.advance(50 * time.Second)
	for range 10 {
		a.Authenticate("admin", "wrong password!")
	}
	c.advance(11 * time.Second)
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Errorf("failures while locked made the lockout longer: %v", err)
	}
}

// TestLockoutUnknownNames locks names without an account like those of
// accounts, so that a lockout does not tell which exist.
func TestLockoutUnknownNames(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	for range DefaultPolicy.LockoutThreshold {
		a.Authenticate("ghost", pw)
	}
	if !a.Locked("ghost") {
		t.Error("an unknown name is not locked")
	}
}

// TestConcurrentGuesses sends many wrong passwords at once: no more of them
// are hashed than LockoutThreshold.
func TestConcurrentGuesses(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Iterations = 50000 // slow enough for the guesses to overlap
	a.Create("admin", pw, Administrator)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { a.Authenticate("admin", "wrong password!") })
	}
	wg.Wait()
	if n := a.hashed.Load(); n > int64(DefaultPolicy.LockoutThreshold) {
		t.Errorf("%d guesses hashed, want at most %d", n, DefaultPolicy.LockoutThreshold)
	}
	if !a.Locked("admin") {
		t.Error("not locked")
	}
}

func TestBusy(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	defer func(d time.Duration) { verifyWait = d }(verifyWait)
	verifyWait = 10 * time.Millisecond
	for range verifySlots {
		a.slots <- struct{}{}
	}
	if _, err := a.Authenticate("admin", pw); err != ErrBusy {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	if a.failures["admin"] != nil {
		t.Error("a busy login counted as a failure")
	}
}

// TestLongPassword refuses a password longer than MaxPasswordLength without
// hashing it.
func TestLongPassword(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	before := a.hashed.Load()
	if _, err := a.Authenticate("admin", strings.Repeat("x", 1<<20)); err == nil {
		t.Fatal("a 1 MiB password logged in")
	}
	if a.hashed.Load() != before {
		t.Error("a password longer than MaxPasswordLength was hashed")
	}
}

func TestChangeIsAtomic(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	a.Create("op", pw, Operator)
	role, short := ReadOnly, "short"
	if err := a.Change("op", &role, nil, &short); err == nil {
		t.Fatal("a too short password was accepted")
	}
	if acc, _ := a.Get("op"); acc.RoleId != Operator {
		t.Errorf("role changed by a refused change: %s", acc.RoleId)
	}
}

func TestVerifyBounds(t *testing.T) {
	b64 := "AAAAAAAAAAAAAAAAAAAAAA"
	for _, h := range []string{
		"$pbkdf2-sha256$2000000000$" + b64 + "$" + b64,
		"$pbkdf2-sha256$1000$" + b64 + "$" + strings.Repeat("A", 1<<20),
	} {
		start := time.Now()
		if verify(h, pw) || time.Since(start) > time.Second {
			t.Errorf("verify(%q) took %v", h[:40], time.Since(start))
		}
	}
}

// TestSlots sends many right passwords of different accounts at once: no
// more than verifySlots are hashed at a time, and no more than maxWaiting
// wait.
func TestSlots(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Iterations = 50000
	for i := range 6 {
		a.Create(fmt.Sprintf("user%d", i), pw, ReadOnly)
	}
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			if _, err := a.Authenticate(fmt.Sprintf("user%d", i), pw); err != nil {
				t.Errorf("user%d: %v", i, err)
			}
		})
	}
	wg.Wait()
	if n := a.maxSeen.Load(); n < 1 || n > 2 {
		t.Errorf("%d passwords hashed at once, want 1 or 2", n)
	}
	// Logins beyond maxWaiting do not wait.
	for range verifySlots {
		a.slots <- struct{}{}
	}
	a.waiting.Store(maxWaiting)
	start := time.Now()
	if _, err := a.Authenticate("user0", "not the password"); err != ErrBusy || time.Since(start) > time.Second {
		t.Errorf("login beyond maxWaiting: %v after %v", err, time.Since(start))
	}
}

// TestSuccessResetsFailures checks that a right password forgets the failures
// before it.
func TestSuccessResetsFailures(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	for range DefaultPolicy.LockoutThreshold - 1 {
		a.Authenticate("admin", "wrong password!")
	}
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Fatal(err)
	}
	for range DefaultPolicy.LockoutThreshold - 1 {
		a.Authenticate("admin", "wrong password!")
	}
	if a.Locked("admin") {
		t.Error("failures before a successful login were kept")
	}
}

// TestFailuresOfAccountsAlwaysCount fills the failures of unknown names with
// locked ones: the names of accounts are still counted and locked.
func TestFailuresOfAccountsAlwaysCount(t *testing.T) {
	defer func(n int) { maxFailures = n }(maxFailures)
	maxFailures = 20
	a, _ := newAccounts(t, &Memory{})
	p := a.Policy()
	p.LockoutThreshold, p.LockoutDuration = 1, 24*time.Hour
	a.SetPolicy(p)
	a.Create("admin", pw, Administrator)
	for i := range 30 {
		a.Authenticate(fmt.Sprintf("junk%d", i), pw)
	}
	if n := len(a.failures); n > maxFailures {
		t.Errorf("%d names counted, want at most %d", n, maxFailures)
	}
	a.Authenticate("admin", "wrong password!")
	if !a.Locked("admin") {
		t.Error("an account was not locked while the failures of unknown names were full")
	}
}

func TestPolicyAndPasswordRules(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	p := a.Policy()
	p.LockoutCounterResetAfter = 0
	if err := a.SetPolicy(p); err == nil {
		t.Error("AccountLockoutCounterResetAfter 0 with a threshold was accepted")
	}
	if err := a.Create("admin", "invalid \xff utf-8 here", Administrator); err == nil {
		t.Error("a password that is not UTF-8 was accepted")
	}
	// Lengths count characters, not bytes.
	if err := a.Create("admin", strings.Repeat("ä", 6), Administrator); err == nil {
		t.Error("6 two-byte characters were accepted")
	}
	if err := a.Create("admin", strings.Repeat("ä", 100), Administrator); err != nil {
		t.Errorf("100 two-byte characters: %v", err)
	}
}

// TestUnknownNameTiming checks that an unknown name is checked against a
// hash as costly as that of an account, also with fewer iterations.
func TestUnknownNameTiming(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Iterations = 200000
	a.Create("admin", pw, Administrator)
	time1 := func(name string) time.Duration {
		start := time.Now()
		a.Authenticate(name, "wrong password!")
		return time.Since(start)
	}
	time1("admin")
	known, unknown := time1("admin"), time1("nobody")
	if unknown > 3*known || known > 3*unknown {
		t.Errorf("unknown name %v, known %v", unknown, known)
	}
}

// toggleStorage fails Save while fail is set.
type toggleStorage struct {
	Memory
	fail bool
}

func (s *toggleStorage) Save(data []byte) error {
	if s.fail {
		return errors.New("disk full")
	}
	return s.Memory.Save(data)
}

// TestPolicyRoundTrip refuses policies that the database cannot store as
// they are.
func TestPolicyRoundTrip(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	for name, p := range map[string]Policy{
		"negative ResetAfter without lockout": {LockoutThreshold: 0, LockoutCounterResetAfter: -time.Second},
		"fractional duration":                 {LockoutDuration: 90*time.Second + time.Second/2},
		"fractional SessionTimeout":           {SessionTimeout: 60*time.Second + 1},
	} {
		q := DefaultPolicy
		if p.LockoutCounterResetAfter != 0 {
			q.LockoutThreshold, q.LockoutCounterResetAfter = 0, p.LockoutCounterResetAfter
		}
		if p.LockoutDuration != 0 {
			q.LockoutDuration = p.LockoutDuration
		}
		if p.SessionTimeout != 0 {
			q.SessionTimeout = p.SessionTimeout
		}
		if err := a.SetPolicy(q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// What is accepted loads again.
	m := &Memory{}
	b, _ := newAccounts(t, m)
	q := DefaultPolicy
	q.LockoutThreshold, q.LockoutCounterResetAfter, q.LockoutDuration = 0, 0, 0
	if err := b.SetPolicy(q); err != nil {
		t.Fatal(err)
	}
	if c, err := NewAccounts(m); err != nil || c.Policy() != q {
		t.Errorf("reloaded: %v, %+v", err, c.Policy())
	}
}

func TestLockoutOffUnlocks(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	for range DefaultPolicy.LockoutThreshold {
		a.Authenticate("admin", "wrong password!")
	}
	if !a.Locked("admin") {
		t.Fatal("not locked")
	}
	if err := a.UpdatePolicy(func(p *Policy) { p.LockoutThreshold = 0 }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Errorf("still locked after turning lockout off: %v", err)
	}
}

// waitFor waits until a.waiting reaches n.
func waitFor(t *testing.T, a *Accounts, n int32) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); a.waiting.Load() != n; {
		if time.Now().After(deadline) {
			t.Fatalf("%d waiting, want %d", a.waiting.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// fillSlots takes every hashing slot and returns a function that gives them
// back.
func fillSlots(a *Accounts) func() {
	for range verifySlots {
		a.slots <- struct{}{}
	}
	return func() {
		for range verifySlots {
			<-a.slots
		}
	}
}

// TestPolicyCheckedAfterHashing raises the minimum password length while a
// new password waits to be hashed: it is refused.
func TestPolicyCheckedAfterHashing(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	short := "fifteen chars!!"
	for _, op := range []struct {
		name string
		fn   func() error
	}{
		{"Create", func() error { return a.Create("op", short, Operator) }},
		{"SetPassword", func() error { return a.SetPassword("admin", short) }},
	} {
		a.SetPolicy(DefaultPolicy)
		release := fillSlots(a)
		errc := make(chan error)
		go func() { errc <- op.fn() }()
		waitFor(t, a, 1)
		if err := a.UpdatePolicy(func(p *Policy) { p.MinPasswordLength = 20 }); err != nil {
			t.Fatal(err)
		}
		release()
		var e *server.Error
		if err := <-errc; !errors.As(err, &e) || e.Status != http.StatusBadRequest || !strings.Contains(e.Message, "20 to 128 characters") {
			t.Errorf("%s: %v, want the error of the new minimum", op.name, err)
		}
	}
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Errorf("admin's password changed: %v", err)
	}
}

func TestDeleteFailsKeepsLockout(t *testing.T) {
	st := &toggleStorage{}
	a, _ := newAccounts(t, st)
	a.Create("admin", pw, Administrator)
	a.Create("op", pw, Operator)
	for range DefaultPolicy.LockoutThreshold {
		a.Authenticate("op", "wrong password!")
	}
	st.fail = true
	if err := a.Delete("op"); err == nil {
		t.Fatal("Delete succeeded without saving")
	}
	if !a.Locked("op") {
		t.Error("a failed Delete unlocked the account")
	}
}

// TestRememberedPasswordKeepsFailures checks that a password matching a
// remembered one does not reset the failures, so that a client polling with
// HTTP Basic does not give others unlimited guesses.
func TestRememberedPasswordKeepsFailures(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	a.Authenticate("admin", pw)
	for range DefaultPolicy.LockoutThreshold - 1 {
		a.Authenticate("admin", "wrong password!")
		if _, err := a.Authenticate("admin", pw); err != nil {
			t.Fatal(err)
		}
	}
	a.Authenticate("admin", "wrong password!")
	if !a.Locked("admin") {
		t.Error("remembered logins in between reset the failures")
	}
}

// TestLockoutFromReservationTime locks a name with a login that waited for a
// slot: the lockout lasts from when the login was counted.
func TestLockoutFromReservationTime(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	a.SetPolicy(Policy{MinPasswordLength: 12, MaxPasswordLength: 128, LockoutThreshold: 1,
		LockoutDuration: time.Minute, LockoutCounterResetAfter: time.Minute, BasicAuth: BasicEnabled, SessionTimeout: time.Hour})
	a.Create("admin", pw, Administrator)
	release := fillSlots(a)
	done := make(chan struct{})
	go func() { a.Authenticate("admin", "wrong password!"); close(done) }()
	waitFor(t, a, 1)
	c.advance(4 * time.Second)
	release()
	<-done
	c.advance(58 * time.Second) // 62 s after the request, 58 s after it was counted
	if !a.Locked("admin") {
		t.Error("the lockout counted from the arrival of the request")
	}
}

// TestReservations takes back failures in the window they were counted in.
func TestReservations(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	a.SetPolicy(Policy{MinPasswordLength: 12, MaxPasswordLength: 128, LockoutThreshold: 2,
		LockoutDuration: time.Minute, LockoutCounterResetAfter: time.Minute, BasicAuth: BasicEnabled, SessionTimeout: time.Hour})
	now := c.now()
	a.mu.Lock()
	defer a.mu.Unlock()

	// A reservation that locked the name is undone completely.
	a.countFailure("x", now)
	res := a.countFailure("x", now)
	if !a.locked("x", now) {
		t.Fatal("second failure did not lock")
	}
	a.undo("x", res)
	if a.locked("x", now) || a.failures["x"].win.count != 1 {
		t.Errorf("after undo: locked %v, count %d, want unlocked, 1", a.locked("x", now), a.failures["x"].win.count)
	}

	// After Unlock and new failures, an old reservation touches nothing.
	old := a.countFailure("y", now)
	delete(a.failures, "y") // Unlock
	a.countFailure("y", now)
	a.countFailure("y", now)
	a.undo("y", old)
	a.clearWindow("y", old)
	if !a.locked("y", now) {
		t.Error("an old reservation undid or cleared a newer lockout")
	}

	// Two logins that locked the name are both taken back, in either
	// order: no failure is left over.
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		delete(a.failures, "z")
		r := [2]reservation{a.countFailure("z", now), a.countFailure("z", now)}
		a.undo("z", r[order[0]])
		a.undo("z", r[order[1]])
		if a.locked("z", now) || a.failures["z"].win.count != 0 {
			t.Errorf("order %v: locked %v, count %d, want unlocked, 0", order, a.locked("z", now), a.failures["z"].win.count)
		}
		a.countFailure("z", now)
		if a.locked("z", now) {
			t.Errorf("order %v: one failure locked the name", order)
		}
	}

	// A login with the right password that helped reach the threshold
	// lifts the lock: the other failure alone does not reach it.
	delete(a.failures, "w")
	ok := a.countFailure("w", now)
	a.countFailure("w", now)
	a.clearWindow("w", ok)
	if a.locked("w", now) {
		t.Error("the lock stands on a failure that was not one")
	}
}

// TestLockFromAnEarlierWindowStands: a login with the right password counted
// before the count was reset does not lift a lock reached by later failures.
func TestLockFromAnEarlierWindowStands(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	a.SetPolicy(Policy{MinPasswordLength: 12, MaxPasswordLength: 128, LockoutThreshold: 2,
		LockoutDuration: time.Minute, LockoutCounterResetAfter: 10 * time.Second, BasicAuth: BasicEnabled, SessionTimeout: time.Hour})
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.countFailure("x", c.now())
	c.advance(11 * time.Second)
	a.countFailure("x", c.now())
	a.countFailure("x", c.now())
	a.clearWindow("x", old)
	if !a.locked("x", c.now()) {
		t.Error("a login from an earlier window lifted the lock")
	}
}

// TestUndoOrderAfterDisable is the review case: two logins with the right
// password are counted, the second locks the name, the account is disabled
// while both are hashed, and both are taken back. Once it is enabled again,
// one failure does not lock it.
func TestUndoOrderAfterDisable(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.SetPolicy(Policy{MinPasswordLength: 12, MaxPasswordLength: 128, LockoutThreshold: 2,
		LockoutDuration: time.Minute, LockoutCounterResetAfter: time.Minute, BasicAuth: BasicEnabled, SessionTimeout: time.Hour})
	a.Create("admin", pw, Administrator)
	a.Create("op", pw, Operator)
	var arrived sync.WaitGroup
	arrived.Add(2)
	gate := make(chan struct{})
	a.beforeCheck = func() { arrived.Done(); <-gate }
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := a.Authenticate("op", pw); err == nil {
				t.Error("login of a disabled account")
			}
		})
	}
	arrived.Wait()
	off, on := false, true
	if err := a.Change("op", nil, &off, nil); err != nil {
		t.Fatal(err)
	}
	close(gate)
	wg.Wait()
	a.beforeCheck = nil
	if err := a.Change("op", nil, &on, nil); err != nil {
		t.Fatal(err)
	}
	a.Authenticate("op", "wrong password!")
	if a.Locked("op") {
		t.Error("one failure locked the account: a taken-back login still counts")
	}
}

// TestMixedIterations loads accounts whose hashes have different iteration
// counts: each gets one with the current count at its next login, so that
// unknown names, checked with the current count, take as long.
func TestMixedIterations(t *testing.T) {
	h1, _ := hash(pw, 1000)
	h2, _ := hash(pw, 2000)
	m := &Memory{}
	m.Save([]byte("account a1 Administrator enabled " + h1 + "\naccount a2 ReadOnly enabled " + h2 + "\n"))
	a, _ := newAccounts(t, m)
	a.Iterations = 3000
	gen := a.list[0].gen
	for _, name := range []string{"a1", "a2"} {
		if _, err := a.Authenticate(name, pw); err != nil {
			t.Fatal(err)
		}
	}
	for _, acc := range a.list {
		if n := hashIterations(acc.hash); n != 3000 {
			t.Errorf("%s: %d iterations after login, want 3000", acc.UserName, n)
		}
	}
	if a.list[0].gen != gen {
		t.Error("the new hash ended the account's sessions")
	}
	a.mu.Lock()
	if n := hashIterations(a.unknownHash()); n != 3000 {
		t.Errorf("unknown names: %d iterations, want 3000", n)
	}
	a.mu.Unlock()
	if _, err := a.Authenticate("a2", pw); err != nil {
		t.Errorf("login with the new hash: %v", err)
	}
	if b, err := NewAccounts(m); err != nil || hashIterations(b.list[1].hash) != 3000 {
		t.Errorf("new hash not stored: %v", err)
	}
}

func TestIterationsBound(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Iterations = maxIterations + 1
	if err := a.Create("admin", pw, Administrator); err == nil {
		t.Error("a hash that cannot be verified was stored")
	}
}

func TestDatabaseErrors(t *testing.T) {
	h, _ := hash(pw, 1000)
	for _, data := range []string{
		"account admin Administrator enabled x\n",
		"account admin Administrator enabled $pbkdf2-sha256$1000$AAAA$AAAA\n",
		"account admin Root enabled " + h + "\n",
	} {
		m := &Memory{}
		m.Save([]byte(data))
		_, err := NewAccounts(m)
		if err == nil {
			t.Errorf("accepted %q", data[:30])
		} else if strings.Contains(err.Error(), "pbkdf2") || strings.Contains(err.Error(), h[20:]) {
			t.Errorf("error shows the hash: %v", err)
		}
	}
}

// TestPasswordWritesUseSlots refuses a new password as busy while the
// hashing slots are taken.
func TestPasswordWritesUseSlots(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	a.Create("admin", pw, Administrator)
	defer func(d time.Duration) { verifyWait = d }(verifyWait)
	verifyWait = 10 * time.Millisecond
	release := fillSlots(a)
	defer release()
	if err := a.SetPassword("admin", "another long password"); !errors.Is(err, ErrBusy) {
		t.Errorf("SetPassword: %v, want ErrBusy", err)
	}
	if err := a.Create("op", pw, Operator); !errors.Is(err, ErrBusy) {
		t.Errorf("Create: %v, want ErrBusy", err)
	}
}

// TestConcurrentRehash logs in twice at once with the right password of an
// account whose hash gets a higher iteration count, both checking the old
// hash: both succeed.
func TestConcurrentRehash(t *testing.T) {
	h, _ := hash(pw, 2000)
	m := &Memory{}
	m.Save([]byte("account admin Administrator enabled " + h + "\n"))
	a, _ := newAccounts(t, m)
	a.Iterations = 3000
	var arrived sync.WaitGroup
	arrived.Add(2)
	a.beforeCheck = func() { arrived.Done(); arrived.Wait() }
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := a.Authenticate("admin", pw); err != nil {
				t.Errorf("concurrent login: %v", err)
			}
		})
	}
	wg.Wait()
	if n := hashIterations(a.list[0].hash); n != 3000 {
		t.Errorf("%d iterations after the logins, want 3000", n)
	}
}

// TestNoDowngrade keeps a hash with more iterations than new ones get.
func TestNoDowngrade(t *testing.T) {
	h, _ := hash(pw, 5000)
	m := &Memory{}
	m.Save([]byte("account admin Administrator enabled " + h + "\n"))
	a, _ := newAccounts(t, m)
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Fatal(err)
	}
	if n := hashIterations(a.list[0].hash); n != 5000 {
		t.Errorf("%d iterations after login, want 5000", n)
	}
}

// TestStaleWindowSameRecord checks that a reservation of an earlier window
// of the same record neither undoes nor clears a later one.
func TestStaleWindowSameRecord(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	a.SetPolicy(Policy{MinPasswordLength: 12, MaxPasswordLength: 128, LockoutThreshold: 3,
		LockoutDuration: time.Minute, LockoutCounterResetAfter: 10 * time.Second, BasicAuth: BasicEnabled, SessionTimeout: time.Hour})
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.countFailure("x", c.now())
	c.advance(11 * time.Second) // the count starts again: a new window, same record
	a.countFailure("x", c.now())
	a.countFailure("x", c.now())
	a.undo("x", old)
	if a.failures["x"].win.count != 2 {
		t.Errorf("count %d after undoing an old window, want 2", a.failures["x"].win.count)
	}
	a.clearWindow("x", old)
	if a.failures["x"] == nil {
		t.Error("an old window's login cleared the new one")
	}
}

// TestUncountedLoginKeepsLockout: a login that was not counted, as lockout
// was off when it began, does not lift a lockout that began meanwhile.
func TestUncountedLoginKeepsLockout(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures["x"] = &failures{win: &window{}, lockedUntil: c.now().Add(time.Minute)}
	a.clearWindow("x", reservation{})
	if !a.locked("x", c.now()) {
		t.Error("an uncounted login lifted a lockout")
	}
}

// TestPolicyLengthAfterWait lowers MaxPasswordLength while a login waits for
// a slot, and after its password was remembered: both are refused.
func TestPolicyLengthAfterWait(t *testing.T) {
	a, _ := newAccounts(t, &Memory{})
	long := strings.Repeat("x", 40)
	a.Create("admin", long, Administrator)
	release := fillSlots(a)
	errc := make(chan error)
	go func() { _, err := a.Authenticate("admin", long); errc <- err }()
	waitFor(t, a, 1)
	a.UpdatePolicy(func(p *Policy) { p.MaxPasswordLength = 30 })
	release()
	if err := <-errc; err == nil {
		t.Error("a login waiting for a slot passed with a password over the new maximum")
	}

	a.UpdatePolicy(func(p *Policy) { p.MaxPasswordLength = 128 })
	a.Unlock("admin")
	if _, err := a.Authenticate("admin", long); err != nil {
		t.Fatal(err)
	}
	// A policy change forgets remembered passwords; remember it again, as
	// a login that got past the length check just before the change would.
	a.UpdatePolicy(func(p *Policy) { p.MaxPasswordLength = 30 })
	a.mu.Lock()
	a.verified[a.cacheKey("admin", long)] = a.now().Add(verifiedTTL)
	a.mu.Unlock()
	if _, err := a.Authenticate("admin", long); err == nil {
		t.Error("a remembered password over the new maximum passed")
	}
}

// TestRehashRetry stops saving new hashes at login for a while after a save
// failed, then saves them again.
func TestRehashRetry(t *testing.T) {
	h, _ := hash(pw, 1000)
	st := &toggleStorage{}
	st.Memory.Save([]byte("account admin Administrator enabled " + h + "\naccount op Operator enabled " + h + "\n"))
	a, c := newAccounts(t, st)
	a.Iterations = 2000
	st.fail = true
	if _, err := a.Authenticate("admin", pw); err != nil {
		t.Fatal(err)
	}
	st.fail = false
	if _, err := a.Authenticate("op", pw); err != nil {
		t.Fatal(err)
	}
	if n := hashIterations(a.list[1].hash); n != 1000 {
		t.Errorf("new hash saved during the cooldown: %d iterations", n)
	}
	c.advance(rehashCooldown)
	a.verified = map[[32]byte]time.Time{}
	if _, err := a.Authenticate("op", pw); err != nil {
		t.Fatal(err)
	}
	if n := hashIterations(a.list[1].hash); n != 2000 {
		t.Errorf("no new hash after the cooldown: %d iterations", n)
	}
}

// TestFailedLoginWork: a failed login with an account whose hash has a lower
// iteration count does as many iterations as one with an unknown name.
func TestFailedLoginWork(t *testing.T) {
	h, _ := hash(pw, 1000)
	m := &Memory{}
	m.Save([]byte("account admin Administrator enabled " + h + "\n"))
	a, _ := newAccounts(t, m)
	a.Iterations = 3000
	work := func(name string) int64 {
		before := a.work.Load()
		a.Authenticate(name, "wrong password!")
		return a.work.Load() - before
	}
	if known, unknown := work("admin"), work("nobody"); known != unknown || unknown != 3000 {
		t.Errorf("iterations: account %d, unknown name %d, want 3000 both", known, unknown)
	}
}

// TestLockLiftGuards keeps a lock, or its window, where taking a failure back
// must not lift it: after the threshold was lowered, and once failures were
// counted in the lock window.
func TestLockLiftGuards(t *testing.T) {
	a, c := newAccounts(t, &Memory{})
	pol := Policy{MinPasswordLength: 12, MaxPasswordLength: 128, LockoutThreshold: 3,
		LockoutDuration: 10 * time.Second, LockoutCounterResetAfter: 10 * time.Second, BasicAuth: BasicEnabled, SessionTimeout: time.Hour}
	a.SetPolicy(pol)
	a.mu.Lock()
	a.countFailure("x", c.now())
	a.countFailure("x", c.now())
	r := a.countFailure("x", c.now())
	a.mu.Unlock()
	a.UpdatePolicy(func(p *Policy) { p.LockoutThreshold = 2 })
	a.mu.Lock()
	a.undo("x", r)
	locked := a.locked("x", c.now())
	a.mu.Unlock()
	if !locked {
		t.Error("lifted a lock whose window still reaches the lowered threshold")
	}

	pol.LockoutThreshold = 2
	a.SetPolicy(pol)
	a.mu.Lock()
	r = a.countFailure("y", c.now())
	a.countFailure("y", c.now())
	lock := a.failures["y"].win
	a.mu.Unlock()
	// Longer windows from now on: the lock expires, but its window is
	// not reset, so the next failure is counted in it.
	a.UpdatePolicy(func(p *Policy) { p.LockoutDuration, p.LockoutCounterResetAfter = time.Minute, time.Minute })
	c.advance(11 * time.Second)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.countFailure("y", c.now())
	a.undo("y", r)
	if a.failures["y"].win != lock || lock.count != 1 {
		t.Error("taking back a failure dropped one counted after the lock")
	}
}

// TestNoRehashOfDisabledAccount: the right password of a disabled account is
// not hashed again.
func TestNoRehashOfDisabledAccount(t *testing.T) {
	h, _ := hash(pw, 1000)
	m := &Memory{}
	m.Save([]byte("account admin Administrator enabled " + h + "\naccount op Operator disabled " + h + "\n"))
	a, _ := newAccounts(t, m)
	a.Iterations = 2000
	before := a.work.Load()
	if _, err := a.Authenticate("op", pw); err == nil {
		t.Fatal("login of a disabled account")
	}
	if n := hashIterations(a.list[1].hash); n != 1000 {
		t.Errorf("disabled account got a new hash: %d iterations", n)
	}
	if w := a.work.Load() - before; w != 1000 {
		t.Errorf("%d iterations, want 1000", w)
	}
}
