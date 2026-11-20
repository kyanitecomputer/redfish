package auth

import (
	"bufio"
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// Privilege is a Redfish privilege, as in the DMTF privilege registry.
type Privilege string

// The standard privileges.
const (
	Login               Privilege = "Login"
	ConfigureManager    Privilege = "ConfigureManager"
	ConfigureUsers      Privilege = "ConfigureUsers"
	ConfigureSelf       Privilege = "ConfigureSelf"
	ConfigureComponents Privilege = "ConfigureComponents"
)

// The predefined roles.
const (
	Administrator = "Administrator"
	Operator      = "Operator"
	ReadOnly      = "ReadOnly"
)

var roles = []struct {
	id         string
	privileges []Privilege
}{
	{Administrator, []Privilege{Login, ConfigureManager, ConfigureUsers, ConfigureSelf, ConfigureComponents}},
	{Operator, []Privilege{Login, ConfigureSelf, ConfigureComponents}},
	{ReadOnly, []Privilege{Login, ConfigureSelf}},
}

// RolePrivileges returns the privileges of a predefined role, or nil for a
// role that does not exist.
func RolePrivileges(role string) []Privilege {
	for _, r := range roles {
		if r.id == role {
			return slices.Clone(r.privileges)
		}
	}
	return nil
}

// Account is a local account.
type Account struct {
	UserName string
	RoleId   string
	Enabled  bool

	// gen changes when the account is created, gets another password or is
	// disabled; sessions opened with an older one have ended.
	gen uint64
}

// Has reports whether the role of the account grants p.
func (a Account) Has(p Privilege) bool {
	return slices.Contains(RolePrivileges(a.RoleId), p)
}

// BasicAuth is whether HTTP Basic authentication is accepted, as
// AccountService.HTTPBasicAuth reports it.
type BasicAuth string

// The HTTPBasicAuth states.
const (
	BasicEnabled      BasicAuth = "Enabled"
	BasicUnadvertised BasicAuth = "Unadvertised" // accepted, but not offered in WWW-Authenticate
	BasicDisabled     BasicAuth = "Disabled"
)

// Policy holds the settings of the account and session services. Accounts
// stores it with the accounts.
type Policy struct {
	// MinPasswordLength and MaxPasswordLength count characters.
	MinPasswordLength int
	MaxPasswordLength int
	// LockoutThreshold is the number of failed logins with a user name that
	// lock it for LockoutDuration; 0 never locks. The count starts again
	// LockoutCounterResetAfter after the first failure.
	LockoutThreshold         int
	LockoutDuration          time.Duration
	LockoutCounterResetAfter time.Duration
	BasicAuth                BasicAuth
	// SessionTimeout ends a session that is not used for that long.
	SessionTimeout time.Duration
}

// DefaultPolicy is the policy of an account database that stores none.
var DefaultPolicy = Policy{
	MinPasswordLength:        12,
	MaxPasswordLength:        128,
	LockoutThreshold:         10,
	LockoutDuration:          time.Minute,
	LockoutCounterResetAfter: time.Minute,
	BasicAuth:                BasicEnabled,
	SessionTimeout:           30 * time.Minute,
}

// maxSetting bounds the durations of the policy, in seconds.
const maxSetting = 1 << 30

// check reports a policy that the account service cannot use.
func (p Policy) check() error {
	switch {
	case p.MinPasswordLength < 1 || p.MaxPasswordLength < p.MinPasswordLength || p.MaxPasswordLength > 1024:
		return badProperty("The password lengths must satisfy 1 <= MinPasswordLength <= MaxPasswordLength <= 1024.")
	case p.LockoutThreshold < 0 || p.LockoutThreshold > maxSetting ||
		p.LockoutDuration < 0 || p.LockoutDuration > maxSetting*time.Second:
		return badProperty("The account lockout settings must be between 0 and 2^30.")
	case p.LockoutThreshold > 0 && (p.LockoutCounterResetAfter < time.Second || p.LockoutCounterResetAfter > p.LockoutDuration):
		return badProperty("AccountLockoutCounterResetAfter must be at least 1 and at most AccountLockoutDuration.")
	case p.BasicAuth != BasicEnabled && p.BasicAuth != BasicUnadvertised && p.BasicAuth != BasicDisabled:
		return badProperty("HTTPBasicAuth must be Enabled, Unadvertised or Disabled.")
	case p.SessionTimeout < 30*time.Second || p.SessionTimeout > 24*time.Hour:
		return badProperty("SessionTimeout must be between 30 and 86400 seconds.")
	}
	return nil
}

// Storage keeps the account database of Accounts.
type Storage interface {
	// Load returns the stored database, or nil when none is stored yet.
	Load() ([]byte, error)
	// Save replaces the stored database. When it fails, the stored database
	// must be the previous one.
	Save(data []byte) error
}

// Memory is a Storage that keeps the database in memory only.
type Memory struct {
	mu   sync.Mutex
	data []byte
}

// Load implements Storage.
func (m *Memory) Load() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.data), nil
}

// Save implements Storage.
func (m *Memory) Save(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = slices.Clone(data)
	return nil
}

const (
	// DefaultIterations is the PBKDF2-HMAC-SHA256 iteration count of new
	// password hashes, as OWASP recommends.
	DefaultIterations = 600000
	// maxIterations bounds the iteration count of a stored hash, so that a
	// tampered database cannot stall logins.
	maxIterations = 10000000

	// verifiedTTL is how long a verified password is remembered, so that
	// HTTP Basic authentication does not hash the password of every request.
	verifiedTTL = 5 * time.Minute
	// maxVerified bounds the remembered passwords.
	maxVerified = 1024

	// verifySlots is how many passwords are hashed at a time, so that an
	// attacker who sends logins cannot take more CPU than that, and
	// maxWaiting how many logins wait for a slot; more get ErrBusy at once.
	// A flood of logins thus delays or refuses the logins with a password
	// of everyone, but not sessions, client certificates and the HTTP Basic
	// requests whose password is remembered.
	verifySlots = 2
	maxWaiting  = 8
)

// verifyWait is how long a login waits for a slot to hash its password.
var verifyWait = 5 * time.Second

// maxFailures bounds the user names without an account whose failed logins
// are counted; those of accounts are always counted.
var maxFailures = 4096

// ErrBusy is the error of a login that waited too long to have its password
// checked, as too many logins arrived at once.
var ErrBusy = errors.New("auth: too many logins at once")

// errLogin is the error of every failed login, whatever the reason, so that
// it does not tell which user names exist.
var errLogin = errors.New("auth: invalid user name or password")

// Accounts holds the local accounts and the policy of the account service.
// Passwords are kept as salted PBKDF2-HMAC-SHA256 hashes. Every change is
// saved to the Storage before it takes effect.
//
// Failed logins are counted per user name, whether an account has it or
// not, so that a lockout does not tell which names exist either.
type Accounts struct {
	// Iterations is the PBKDF2 iteration count of new password hashes;
	// DefaultIterations when zero. Hashes keep the count they were made with.
	// Set it before using the Accounts.
	Iterations int

	storage Storage
	now     func() time.Time
	slots   chan struct{}
	waiting atomic.Int32
	hashed  atomic.Int64 // passwords hashed, for tests
	active  atomic.Int32 // passwords being hashed
	maxSeen atomic.Int32 // most passwords hashed at once, for tests

	mu       sync.Mutex
	policy   Policy
	list     []*account
	failures map[string]*failures
	verified map[[32]byte]time.Time // remembered passwords, by cacheKey
	key      [32]byte               // secret of cacheKey
	dummy    string                 // the hash checked for unknown names while there are no accounts
}

type account struct {
	Account
	hash string
}

type failures struct {
	count       int
	first       time.Time
	lockedUntil time.Time
}

// NewAccounts returns the accounts stored in s, with DefaultPolicy when s holds
// no database yet. A service without accounts lets nobody in: create the
// first Administrator with Create, for example from a password provisioned
// with the device.
func NewAccounts(s Storage) (*Accounts, error) {
	a := &Accounts{
		storage:  s,
		now:      time.Now,
		slots:    make(chan struct{}, verifySlots),
		policy:   DefaultPolicy,
		failures: map[string]*failures{},
		verified: map[[32]byte]time.Time{},
	}
	if _, err := rand.Read(a.key[:]); err != nil {
		return nil, err
	}
	data, err := s.Load()
	if err != nil {
		return nil, err
	}
	if data != nil {
		if a.list, a.policy, err = parse(data); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *Accounts) iterations() int {
	if a.Iterations > 0 {
		return a.Iterations
	}
	return DefaultIterations
}

// Policy returns the policy of the account service.
func (a *Accounts) Policy() Policy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy
}

// SetPolicy changes the policy of the account service.
func (a *Accounts) SetPolicy(p Policy) error {
	if err := p.check(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commit(a.list, p)
}

// List returns the accounts in the order they were created.
func (a *Accounts) List() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Account, len(a.list))
	for i, acc := range a.list {
		out[i] = acc.Account
	}
	return out
}

// Get returns the account named name.
func (a *Accounts) Get(name string) (Account, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if acc := a.find(name); acc != nil {
		return acc.Account, true
	}
	return Account{}, false
}

// Locked reports whether failed logins have locked the user name.
func (a *Accounts) Locked(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.locked(name, a.now())
}

// Unlock forgets the failed logins with the user name.
func (a *Accounts) Unlock(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, name)
}

// Create adds an enabled account. The name must be 1 to 32 letters, digits,
// '.', '_' and '-', starting with a letter or digit, as it is also the Id of
// the account's resource.
func (a *Accounts) Create(name, password, role string) error {
	return a.create(name, password, role, true)
}

func (a *Accounts) create(name, password, role string, enabled bool) error {
	if err := checkName(name); err != nil {
		return err
	}
	if RolePrivileges(role) == nil {
		return badRole(role)
	}
	if err := checkPassword(a.Policy(), password); err != nil {
		return err
	}
	h, err := hash(password, a.iterations())
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.find(name) != nil {
		return errExists(name)
	}
	acc := &account{Account: Account{UserName: name, RoleId: role, Enabled: enabled, gen: newGen()}, hash: h}
	return a.commit(append(slices.Clone(a.list), acc), a.policy)
}

// SetPassword changes the password of the account named name.
func (a *Accounts) SetPassword(name, password string) error {
	return a.Change(name, nil, nil, &password)
}

// Update changes the role or the enabled state of the account named name,
// where role or enabled is not nil.
func (a *Accounts) Update(name string, role *string, enabled *bool) error {
	return a.Change(name, role, enabled, nil)
}

// Change changes the role, the enabled state and the password of the account
// named name, where they are not nil, all or none. The last enabled
// Administrator stays one.
func (a *Accounts) Change(name string, role *string, enabled *bool, password *string) error {
	_, err := a.change(name, role, enabled, password)
	return err
}

// change is Change, returning the account as changed.
func (a *Accounts) change(name string, role *string, enabled *bool, password *string) (Account, error) {
	if role != nil && RolePrivileges(*role) == nil {
		return Account{}, badRole(*role)
	}
	var h string
	if password != nil {
		if err := checkPassword(a.Policy(), *password); err != nil {
			return Account{}, err
		}
		var err error
		if h, err = hash(*password, a.iterations()); err != nil {
			return Account{}, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.list, func(acc *account) bool { return acc.UserName == name })
	if i < 0 {
		return Account{}, errNoAccount(name)
	}
	acc := *a.list[i]
	if role != nil {
		acc.RoleId = *role
	}
	if enabled != nil {
		if acc.Enabled && !*enabled {
			acc.gen = newGen()
		}
		acc.Enabled = *enabled
	}
	if password != nil {
		acc.hash, acc.gen = h, newGen()
	}
	list := slices.Clone(a.list)
	list[i] = &acc
	if adminCount(a.list) > 0 && adminCount(list) == 0 {
		return Account{}, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgGeneralError,
			Message:   "The last enabled Administrator account cannot be disabled or given another role.",
		}
	}
	if err := a.commit(list, a.policy); err != nil {
		return Account{}, err
	}
	return acc.Account, nil
}

// Delete removes the account named name, unless it is the last enabled
// Administrator.
func (a *Accounts) Delete(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.list, func(acc *account) bool { return acc.UserName == name })
	if i < 0 {
		return errNoAccount(name)
	}
	list := slices.Delete(slices.Clone(a.list), i, i+1)
	if adminCount(a.list) > 0 && adminCount(list) == 0 {
		return &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgResourceCannotBeDeleted,
			Message:   "The last enabled Administrator account cannot be deleted.",
		}
	}
	delete(a.failures, name)
	return a.commit(list, a.policy)
}

func adminCount(list []*account) int {
	n := 0
	for _, acc := range list {
		if acc.Enabled && acc.RoleId == Administrator {
			n++
		}
	}
	return n
}

// commit saves list and policy and makes them current. Remembered passwords
// are forgotten, as an account may have changed. a.mu must be held.
func (a *Accounts) commit(list []*account, policy Policy) error {
	if err := a.storage.Save(format(list, policy)); err != nil {
		return err
	}
	a.list, a.policy = list, policy
	clear(a.verified)
	return nil
}

func (a *Accounts) find(name string) *account {
	for _, acc := range a.list {
		if acc.UserName == name {
			return acc
		}
	}
	return nil
}

// Authenticate checks the password of the account named name and returns the
// account. It fails for an unknown, disabled or locked user name, without
// telling which, and with ErrBusy when too many logins wait.
//
// A login whose password is not remembered as verified first waits for a
// slot to hash it, then counts as a failure of the user name, and is taken
// back when the password is right: concurrent logins get no more tries than
// LockoutThreshold, and a login refused as busy is not counted. While the
// name is locked, attempts fail without being counted, so that the lockout
// ends LockoutDuration after it began whatever is sent meanwhile.
func (a *Accounts) Authenticate(name, password string) (Account, error) {
	now := a.now()
	a.mu.Lock()
	maxLen := a.policy.MaxPasswordLength
	a.mu.Unlock()
	if utf8.RuneCountInString(password) > maxLen {
		a.mu.Lock()
		if !a.locked(name, now) {
			a.countFailure(name, now)
		}
		a.mu.Unlock()
		return Account{}, errLogin
	}
	key := a.cacheKey(name, password)
	a.mu.Lock()
	if a.locked(name, now) {
		a.mu.Unlock()
		return Account{}, errLogin
	}
	if acc := a.find(name); acc != nil && acc.Enabled && a.verified[key].After(now) {
		a.mu.Unlock()
		return acc.Account, nil
	}
	a.mu.Unlock()

	if err := a.acquire(); err != nil {
		return Account{}, err
	}
	defer a.release()
	a.mu.Lock()
	if a.locked(name, now) {
		a.mu.Unlock()
		return Account{}, errLogin
	}
	a.countFailure(name, now)
	acc := a.find(name)
	h := a.unknownHash()
	if acc != nil {
		h = acc.hash
	}
	a.mu.Unlock()

	ok := a.check(h, password)

	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.find(name)
	switch {
	case acc == nil || !ok:
		return Account{}, errLogin
	case cur == nil || cur.hash != h:
		// The account changed while the password was hashed: not a
		// failure of the client.
		a.refund(name)
		return Account{}, errLogin
	case !cur.Enabled:
		return Account{}, errLogin
	}
	delete(a.failures, name)
	if len(a.verified) >= maxVerified {
		clear(a.verified)
	}
	a.verified[key] = now.Add(verifiedTTL)
	return cur.Account, nil
}

// acquire waits for a slot to hash a password, or fails with ErrBusy after
// verifyWait or when maxWaiting logins wait already.
func (a *Accounts) acquire() error {
	if a.waiting.Add(1) > maxWaiting {
		a.waiting.Add(-1)
		return ErrBusy
	}
	defer a.waiting.Add(-1)
	t := time.NewTimer(verifyWait)
	defer t.Stop()
	select {
	case a.slots <- struct{}{}:
		return nil
	case <-t.C:
		return ErrBusy
	}
}

func (a *Accounts) release() { <-a.slots }

// check reports whether password matches h; the caller holds a slot.
func (a *Accounts) check(h, password string) bool {
	a.hashed.Add(1)
	n := a.active.Add(1)
	defer a.active.Add(-1)
	for m := a.maxSeen.Load(); n > m && !a.maxSeen.CompareAndSwap(m, n); m = a.maxSeen.Load() {
	}
	return verify(h, password)
}

// unknownHash returns the hash to check the password of an unknown user name
// against, one that takes as long as a real one: that of the first account,
// or while there is none, one made for the purpose. a.mu must be held.
func (a *Accounts) unknownHash() string {
	if len(a.list) > 0 {
		return a.list[0].hash
	}
	if f := strings.Split(a.dummy, "$"); len(f) != 5 || f[2] != strconv.Itoa(a.iterations()) {
		a.dummy, _ = hash(rand.Text(), a.iterations())
	}
	return a.dummy
}

// locked reports whether the user name is locked. a.mu must be held.
func (a *Accounts) locked(name string, now time.Time) bool {
	f := a.failures[name]
	return f != nil && now.Before(f.lockedUntil)
}

// countFailure counts a failed login with the user name, and locks it when
// that reaches the threshold. The names of accounts are always counted;
// when maxFailures other names are counted, those that are not locked are
// forgotten, and while that leaves no room, other names are not counted.
// a.mu must be held.
func (a *Accounts) countFailure(name string, now time.Time) {
	p := a.policy
	if p.LockoutThreshold == 0 {
		return
	}
	f := a.failures[name]
	if f == nil {
		if a.find(name) == nil && a.unknownFailures() >= maxFailures {
			for n, old := range a.failures {
				if a.find(n) == nil && !now.Before(old.lockedUntil) {
					delete(a.failures, n)
				}
			}
			if a.unknownFailures() >= maxFailures {
				return
			}
		}
		f = &failures{first: now}
		a.failures[name] = f
	} else if !now.Before(f.first.Add(p.LockoutCounterResetAfter)) {
		f.count, f.first = 0, now
	}
	f.count++
	if f.count >= p.LockoutThreshold {
		f.lockedUntil = now.Add(p.LockoutDuration)
		f.count, f.first = 0, now
	}
}

// unknownFailures returns how many counted names have no account. a.mu must
// be held.
func (a *Accounts) unknownFailures() int {
	n := len(a.failures)
	for _, acc := range a.list {
		if a.failures[acc.UserName] != nil {
			n--
		}
	}
	return n
}

// refund takes back a failure that countFailure counted for an attempt that
// did not fail through the client. a.mu must be held.
func (a *Accounts) refund(name string) {
	if f := a.failures[name]; f != nil && f.count > 0 {
		f.count--
	}
}

func (a *Accounts) cacheKey(name, password string) [32]byte {
	h := sha256.New()
	h.Write(a.key[:])
	h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(name))))
	h.Write([]byte(name))
	h.Write([]byte(password))
	return [32]byte(h.Sum(nil))
}

func newGen() uint64 {
	var b [8]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint64(b[:])
}

// hash returns the PBKDF2 hash of password with a new salt.
func hash(password string, iter int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$pbkdf2-sha256$%d$%s$%s", iter, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verify reports whether password matches hash.
func verify(hash, password string) bool {
	f := strings.Split(hash, "$")
	if len(f) != 5 || f[0] != "" || f[1] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(f[2])
	if err != nil || iter < 1 || iter > maxIterations {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(f[3])
	want, err2 := b64.DecodeString(f[4])
	if err1 != nil || err2 != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func checkName(name string) error {
	ok := len(name) >= 1 && len(name) <= 32 && !strings.HasPrefix(name, ".") &&
		!strings.HasPrefix(name, "_") && !strings.HasPrefix(name, "-") &&
		!strings.ContainsFunc(name, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
		})
	if !ok {
		return badProperty("The user name must be 1 to 32 letters, digits, '.', '_' and '-', starting with a letter or digit.")
	}
	return nil
}

func checkPassword(p Policy, password string) error {
	if n := utf8.RuneCountInString(password); !utf8.ValidString(password) || n < p.MinPasswordLength || n > p.MaxPasswordLength {
		return badProperty(fmt.Sprintf("The password must be UTF-8 with %d to %d characters.", p.MinPasswordLength, p.MaxPasswordLength))
	}
	return nil
}

func badProperty(msg string) error {
	return &server.Error{Status: http.StatusBadRequest, MessageID: odata.MsgPropertyValueFormatError, Message: msg}
}

func badRole(role string) error {
	return &server.Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgPropertyValueNotInList,
		Message:   "The value " + role + " for the property RoleId is not in the list of acceptable values: Administrator, Operator, ReadOnly.",
	}
}

func errExists(name string) error {
	return &server.Error{
		Status:    http.StatusConflict,
		MessageID: odata.MsgResourceAlreadyExists,
		Message:   "An account with the user name " + name + " already exists.",
	}
}

func errNoAccount(name string) error {
	return &server.Error{
		Status:    http.StatusNotFound,
		MessageID: odata.MsgResourceNotFound,
		Message:   "The account " + name + " does not exist.",
	}
}

// The stored database is text, one setting or account per line:
//
//	# redfish accounts
//	policy MinPasswordLength 12
//	account admin Administrator enabled $pbkdf2-sha256$600000$<salt>$<key>
const header = "# redfish accounts"

func format(list []*account, p Policy) []byte {
	var b bytes.Buffer
	fmt.Fprintln(&b, header)
	fmt.Fprintf(&b, "policy MinPasswordLength %d\n", p.MinPasswordLength)
	fmt.Fprintf(&b, "policy MaxPasswordLength %d\n", p.MaxPasswordLength)
	fmt.Fprintf(&b, "policy AccountLockoutThreshold %d\n", p.LockoutThreshold)
	fmt.Fprintf(&b, "policy AccountLockoutDuration %d\n", int64(p.LockoutDuration/time.Second))
	fmt.Fprintf(&b, "policy AccountLockoutCounterResetAfter %d\n", int64(p.LockoutCounterResetAfter/time.Second))
	fmt.Fprintf(&b, "policy HTTPBasicAuth %s\n", p.BasicAuth)
	fmt.Fprintf(&b, "policy SessionTimeout %d\n", int64(p.SessionTimeout/time.Second))
	for _, acc := range list {
		state := "enabled"
		if !acc.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&b, "account %s %s %s %s\n", acc.UserName, acc.RoleId, state, acc.hash)
	}
	return b.Bytes()
}

func parse(data []byte) ([]*account, Policy, error) {
	p := DefaultPolicy
	var list []*account
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		bad := fmt.Errorf("auth: account database line %d: %q", n, line)
		switch {
		case f[0] == "policy" && len(f) == 3:
			if f[1] == "HTTPBasicAuth" {
				p.BasicAuth = BasicAuth(f[2])
				continue
			}
			v, err := strconv.Atoi(f[2])
			if err != nil || v < 0 || v > maxSetting {
				return nil, p, bad
			}
			switch f[1] {
			case "MinPasswordLength":
				p.MinPasswordLength = v
			case "MaxPasswordLength":
				p.MaxPasswordLength = v
			case "AccountLockoutThreshold":
				p.LockoutThreshold = v
			case "AccountLockoutDuration":
				p.LockoutDuration = time.Duration(v) * time.Second
			case "AccountLockoutCounterResetAfter":
				p.LockoutCounterResetAfter = time.Duration(v) * time.Second
			case "SessionTimeout":
				p.SessionTimeout = time.Duration(v) * time.Second
			default:
				return nil, p, bad
			}
		case f[0] == "account" && len(f) == 5:
			acc := &account{Account: Account{UserName: f[1], RoleId: f[2], Enabled: f[3] == "enabled", gen: newGen()}, hash: f[4]}
			if checkName(acc.UserName) != nil || RolePrivileges(acc.RoleId) == nil ||
				(f[3] != "enabled" && f[3] != "disabled") || slices.ContainsFunc(list, func(o *account) bool { return o.UserName == acc.UserName }) {
				return nil, p, bad
			}
			list = append(list, acc)
		default:
			return nil, p, bad
		}
	}
	if err := sc.Err(); err != nil {
		return nil, p, err
	}
	if err := p.check(); err != nil {
		return nil, p, fmt.Errorf("auth: account database: %w", err)
	}
	return list, p, nil
}
