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
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                   { return &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)} }
func status(err error) int               { e, _ := errors.AsType[*server.Error](err); return statusOf(e) }
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
