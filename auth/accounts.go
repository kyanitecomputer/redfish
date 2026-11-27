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
	return slices.Clone(privileges(role))
}

// privileges is RolePrivileges without the copy, which callers must not
// change.
func privileges(role string) []Privilege {
	for _, r := range roles {
		if r.id == role {
			return r.privileges
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
	return slices.Contains(privileges(a.RoleId), p)
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

// check reports a policy that the account service cannot use, or cannot
// store as it is: the durations are stored in whole seconds.
func (p Policy) check() error {
	seconds := func(d time.Duration) bool { return d >= 0 && d <= maxSetting*time.Second && d%time.Second == 0 }
	switch {
	case p.MinPasswordLength < 1 || p.MaxPasswordLength < p.MinPasswordLength || p.MaxPasswordLength > maxPasswordLength:
		return badProperty("The password lengths must satisfy 1 <= MinPasswordLength <= MaxPasswordLength <= 1024.")
	case p.LockoutThreshold < 0 || p.LockoutThreshold > maxSetting ||
		!seconds(p.LockoutDuration) || !seconds(p.LockoutCounterResetAfter) || !seconds(p.SessionTimeout):
		return badProperty("The account lockout settings must be between 0 and 2^30, and durations whole seconds.")
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
	// maxPasswordLength bounds MaxPasswordLength.
	maxPasswordLength = 1024
	// maxIterations bounds the iteration count of a stored hash, so that a
	// tampered database cannot stall logins.
	maxIterations = 10000000

	// verifiedTTL is how long a verified password is remembered, so that
	// HTTP Basic authentication does not hash the password of every request.
	verifiedTTL = 5 * time.Minute
	// maxVerified bounds the remembered passwords.
	maxVerified = 1024
	// rehashCooldown is how long logins do not save a new hash after
	// saving one failed.
	rehashCooldown = 5 * time.Minute

	// verifySlots is how many passwords are hashed at a time, so that an
	// attacker who sends logins cannot take more CPU than that, and
	// maxWaiting how many logins wait for a slot; more get ErrBusy at once.
	// A flood of logins thus delays or refuses the logins with a password
	// of everyone, but not sessions, client certificates and the HTTP Basic
	// requests whose password is remembered.
	verifySlots = 2
	maxWaiting  = 8
)

const (
	// verifyWait is how long a login waits for a slot to hash its password.
	verifyWait = 5 * time.Second
	// maxFailures bounds the user names without an account whose failed
	// logins are counted; those of accounts are always counted.
	maxFailures = 4096
)

// ErrBusy is the error of a login, or a change of a password, that waited
// too long for a slot to hash the password, as too many arrived at once. It
// is a *server.Error with status 503.
var ErrBusy error = &server.Error{
	Status:    http.StatusServiceUnavailable,
	MessageID: odata.MsgServiceUnavailable,
	Message:   "The service is temporarily unavailable: too many logins or password changes at once. Retry later.",
}

// errLogin is the error of every failed login, whatever the reason, so that
// it does not tell which user names exist.
var errLogin = errors.New("auth: invalid user name or password")

// errSuperseded is the error of a change requested with credentials that no
// longer hold: the account of the request got another password, or was
// disabled or deleted, after the request was authenticated.
var errSuperseded error = &server.Error{
	Status:    http.StatusConflict,
	MessageID: odata.MsgGeneralError,
	Message:   "The account of the request changed while the request was processed; nothing was changed. Log in again.",
}

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
	// rehashAfter is when a new hash at login may be saved again after
	// saving one failed, in Unix nanoseconds.
	rehashAfter atomic.Int64
	// verifyWait and maxFailures are the constants of the same names,
	// unless a test changes them.
	verifyWait  time.Duration
	maxFailures int
	probe       *probe // nil but in tests

	mu       sync.Mutex
	policy   Policy
	list     []*account
	failures map[string]*failures
	verified map[[32]byte]time.Time // remembered passwords, by cacheKey
	key      [32]byte               // secret of cacheKey
	dummy    string                 // the hash checked for unknown names
}

type account struct {
	Account
	hash string
}

// failures is the record of the failed logins with a user name.
type failures struct {
	win         *window // the current window
	lockedUntil time.Time
}

// window counts failed logins from first on. A window starts with the first
// failure, again when the count is reset, and when the name is locked. When
// failures that do not count are taken back so that the window before a lock
// no longer reaches the threshold, the lock is lifted and that window is the
// current one again; the window the lock began is dropped, which only
// happens while nothing is counted in it.
type window struct {
	count  int
	first  time.Time
	before *window // for a window begun by a lock, the window that reached the threshold
}

// reservation is a login counted as a failure before its password is
// hashed, so that it can be taken back.
type reservation struct {
	f *failures // nil when the login was not counted
	w *window   // the window it was counted in
}

// NewAccounts returns the accounts stored in s, with DefaultPolicy when s holds
// no database yet. A service without accounts lets nobody in: create the
// first Administrator with Create, for example from a password provisioned
// with the device.
func NewAccounts(s Storage) (*Accounts, error) {
	a := &Accounts{
		storage:     s,
		now:         time.Now,
		verifyWait:  verifyWait,
		maxFailures: maxFailures,
		slots:       make(chan struct{}, verifySlots),
		policy:      DefaultPolicy,
		failures:    map[string]*failures{},
		verified:    map[[32]byte]time.Time{},
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
	return a.UpdatePolicy(func(cur *Policy) { *cur = p })
}

// UpdatePolicy changes the policy of the account service with fn, which
// gets the current policy, all in one step, so that concurrent changes of
// different settings do not undo each other. fn runs with the Accounts
// locked and must not call its methods. Turning lockout off unlocks every
// user name.
func (a *Accounts) UpdatePolicy(fn func(*Policy)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.policy
	fn(&p)
	if err := p.check(); err != nil {
		return err
	}
	if err := a.commit(a.list, p); err != nil {
		return err
	}
	if p.LockoutThreshold == 0 {
		clear(a.failures)
	}
	return nil
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
	_ = a.unlock(name, nil)
}

// unlock is Unlock, failing with errSuperseded as change does.
func (a *Accounts) unlock(name string, by *Account) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.superseded(by) {
		return errSuperseded
	}
	delete(a.failures, name)
	return nil
}

// superseded reports whether by, the account a change was authorized with
// as it was then, no longer holds: it was deleted, or has another
// generation or role. A nil by always holds. a.mu must be held.
func (a *Accounts) superseded(by *Account) bool {
	if by == nil {
		return false
	}
	cur := a.find(by.UserName)
	return cur == nil || cur.gen != by.gen || cur.RoleId != by.RoleId
}

// Create adds an enabled account. The name must be 1 to 32 letters, digits,
// '.', '_' and '-', starting with a letter or digit, as it is also the Id of
// the account's resource.
func (a *Accounts) Create(name, password, role string) error {
	return a.create(name, password, role, true, nil)
}

// create is Create, failing with errSuperseded as change does.
func (a *Accounts) create(name, password, role string, enabled bool, by *Account) error {
	if err := checkName(name); err != nil {
		return err
	}
	if privileges(role) == nil {
		return badRole(role)
	}
	if err := checkPassword(a.Policy(), password); err != nil {
		return err
	}
	h, err := a.hashPassword(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.superseded(by) {
		return errSuperseded
	}
	if a.find(name) != nil {
		return errExists(name)
	}
	// The policy may have changed while the password was hashed.
	if err := checkPassword(a.policy, password); err != nil {
		return err
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
// Administrator stays one. A new password is hashed in one of the slots that
// logins use, so Change fails with ErrBusy when too many wait for one.
func (a *Accounts) Change(name string, role *string, enabled *bool, password *string) error {
	_, _, err := a.change(name, role, enabled, password, nil)
	return err
}

// change is Change, returning the generation of the account before the
// change and the account as changed. If by is not nil, it is the account
// the change was authorized with, as it was then: the change fails with
// errSuperseded, changing nothing, unless that account still exists with
// the same generation and role when the change is saved (disabling an
// account gives it a new generation).
func (a *Accounts) change(name string, role *string, enabled *bool, password *string, by *Account) (uint64, Account, error) {
	if role != nil && privileges(*role) == nil {
		return 0, Account{}, badRole(*role)
	}
	var h string
	if password != nil {
		if err := checkPassword(a.Policy(), *password); err != nil {
			return 0, Account{}, err
		}
		var err error
		if h, err = a.hashPassword(*password); err != nil {
			return 0, Account{}, err
		}
	}
	a.probe.change()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.superseded(by) {
		return 0, Account{}, errSuperseded
	}
	i := slices.IndexFunc(a.list, func(acc *account) bool { return acc.UserName == name })
	if i < 0 {
		return 0, Account{}, errNoAccount(name)
	}
	if password != nil {
		// The policy may have changed while the password was hashed.
		if err := checkPassword(a.policy, *password); err != nil {
			return 0, Account{}, err
		}
	}
	prev := a.list[i].gen
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
		return 0, Account{}, &server.Error{
			Status:    http.StatusBadRequest,
			MessageID: odata.MsgGeneralError,
			Message:   "The last enabled Administrator account cannot be disabled or given another role.",
		}
	}
	if err := a.commit(list, a.policy); err != nil {
		return 0, Account{}, err
	}
	return prev, acc.Account, nil
}

// Delete removes the account named name, unless it is the last enabled
// Administrator.
func (a *Accounts) Delete(name string) error {
	return a.delete(name, nil)
}

// delete is Delete, failing with errSuperseded as change does.
func (a *Accounts) delete(name string, by *Account) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.superseded(by) {
		return errSuperseded
	}
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
	if err := a.commit(list, a.policy); err != nil {
		return err
	}
	delete(a.failures, name)
	return nil
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
//
// A login with the right password takes its failure back and, if the window
// it was counted in is still the current one, forgets the failures of the
// name. A lock that the login's own failure helped reach is lifted, as only
// the others count. A password that matches a remembered one does not reset
// the count of failures: otherwise a client that polls with HTTP Basic would
// let others guess without limit. The count also starts again
// LockoutCounterResetAfter after the first failure.
//
// An unknown name is checked against a hash with the current iteration count
// and one PBKDF2 block. A failed login with an account whose hash takes less
// work, iterations times blocks, does the missing iterations too, so both
// take as long; the account gets a new hash at its next successful login if
// its count is lower. Hashes that take more work, left from a service that
// used a higher count or a longer key, are kept: failed logins with those
// names take longer than with unknown names, which tells that the names
// exist. Logins fail without hashing while Iterations is above the most a
// stored hash may have, even with a remembered password.
func (a *Accounts) Authenticate(name, password string) (Account, error) {
	cost := a.iterations()
	if cost > maxIterations {
		return Account{}, fmt.Errorf("auth: %d PBKDF2 iterations, want 1 to %d", cost, maxIterations)
	}
	// A password longer than any policy allows is not even hashed for the
	// cache key.
	n := utf8.RuneCountInString(password)
	var key [32]byte
	if n <= maxPasswordLength {
		key = a.cacheKey(name, password)
	}
	a.mu.Lock()
	now := a.now()
	if n > a.policy.MaxPasswordLength {
		if !a.locked(name, now) {
			a.countFailure(name, now)
		}
		a.mu.Unlock()
		return Account{}, errLogin
	}
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
	// The time of the reservation, not of the request, which may have
	// waited for the slot.
	now = a.now()
	if a.locked(name, now) {
		a.mu.Unlock()
		return Account{}, errLogin
	}
	res := a.countFailure(name, now)
	acc := a.find(name)
	h := a.unknownHash()
	if acc != nil {
		h = acc.hash
	}
	a.mu.Unlock()

	a.probe.check()
	ph, _ := parseHash(h)
	ok := a.check(ph, password)
	// The cooldown is checked here to save hashing, and again before the
	// save, which decides.
	var rehash string
	switch {
	case ok && acc != nil && acc.Enabled && ph.iter < cost && now.UnixNano() >= a.rehashAfter.Load():
		a.probe.addWork(cost)
		rehash, _ = hash(password, cost)
	case !ok && ph.work() < cost:
		a.pad(cost - ph.work())
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.find(name)
	switch {
	case acc == nil || !ok || n > a.policy.MaxPasswordLength:
		return Account{}, errLogin
	case cur == nil || cur.gen != acc.gen:
		// The account got another password, or was disabled or
		// replaced, while the password was hashed: not a failure of
		// the client. A new hash of the same password by a concurrent
		// login keeps the generation.
		a.undo(name, res)
		return Account{}, errLogin
	case !cur.Enabled:
		return Account{}, errLogin
	}
	a.clearWindow(name, res)
	// Same password, same generation: sessions stay. A database that
	// cannot be saved is tried again only rehashCooldown after the failed
	// save, not with every login: a login that hashed before the failure
	// does not save either.
	if rehash != "" && cur.hash == h && a.now().UnixNano() >= a.rehashAfter.Load() {
		i := slices.Index(a.list, cur)
		up := *cur
		up.hash = rehash
		list := slices.Clone(a.list)
		list[i] = &up
		if a.commit(list, a.policy) == nil {
			cur = &up
		} else {
			a.rehashAfter.Store(a.now().Add(rehashCooldown).UnixNano())
		}
	}
	if len(a.verified) >= maxVerified {
		clear(a.verified)
	}
	a.verified[key] = now.Add(verifiedTTL)
	return cur.Account, nil
}

// hashPassword returns the hash of a new password, made in one of the slots
// of the logins.
func (a *Accounts) hashPassword(password string) (string, error) {
	if err := a.acquire(); err != nil {
		return "", err
	}
	defer a.release()
	return hash(password, a.iterations())
}

// acquire waits for a slot to hash a password, or fails with ErrBusy after
// a.verifyWait or when maxWaiting logins wait already.
func (a *Accounts) acquire() error {
	if a.waiting.Add(1) > maxWaiting {
		a.waiting.Add(-1)
		return ErrBusy
	}
	defer a.waiting.Add(-1)
	t := time.NewTimer(a.verifyWait)
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
func (a *Accounts) check(h pbkdf2Hash, password string) bool {
	if a.probe != nil {
		defer a.probe.hashing(h.work())()
	}
	return h.matches(password)
}

// pad does iter PBKDF2 iterations for nothing; the caller holds a slot.
func (a *Accounts) pad(iter int) {
	a.probe.addWork(iter)
	_, _ = pbkdf2.Key(sha256.New, "", padSalt[:], iter, sha256.Size)
}

var padSalt [16]byte

// unknownHash returns the hash to check the password of an unknown user name
// against: random, so that no password matches, with the current iteration
// count, so that checking it takes as long as a real one. a.mu must be held.
func (a *Accounts) unknownHash() string {
	if iter := a.iterations(); hashIterations(a.dummy) != iter {
		b64 := base64.RawStdEncoding
		a.dummy = fmt.Sprintf("$pbkdf2-sha256$%d$%s$%s", iter, b64.EncodeToString(randomBytes(16)), b64.EncodeToString(randomBytes(sha256.Size)))
	}
	return a.dummy
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// locked reports whether the user name is locked. a.mu must be held.
func (a *Accounts) locked(name string, now time.Time) bool {
	f := a.failures[name]
	return f != nil && now.Before(f.lockedUntil)
}

// countFailure counts a failed login with the user name, and locks it when
// that reaches the threshold. The names of accounts are always counted;
// when a.maxFailures other names are counted, those that are not locked are
// forgotten, and while that leaves no room, other names are not counted.
// It returns what it did, for undo and clearWindow. a.mu must be held.
func (a *Accounts) countFailure(name string, now time.Time) reservation {
	p := a.policy
	if p.LockoutThreshold == 0 {
		return reservation{}
	}
	f := a.failures[name]
	if f == nil {
		if a.find(name) == nil && a.unknownFailures() >= a.maxFailures {
			for n, old := range a.failures {
				if a.find(n) == nil && !now.Before(old.lockedUntil) {
					delete(a.failures, n)
				}
			}
			if a.unknownFailures() >= a.maxFailures {
				return reservation{}
			}
		}
		f = &failures{win: &window{first: now}}
		a.failures[name] = f
	} else if !now.Before(f.win.first.Add(p.LockoutCounterResetAfter)) {
		f.win = &window{first: now}
	}
	w := f.win
	w.count++
	// A lock window with failures in it is no longer lifted by undo.
	w.before = nil
	if w.count >= p.LockoutThreshold {
		f.lockedUntil = now.Add(p.LockoutDuration)
		f.win = &window{first: now, before: w}
	}
	return reservation{f: f, w: w}
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

// undo takes back the failure that res counted, for a login that did not
// fail through the client, in the window it was counted in, even if that is
// no longer the current one. When that leaves the window before the current
// lock below the threshold, and nothing was counted since the lock, the
// lock is lifted and that window is the current one again. A record that
// replaced the one res was counted in, after Unlock or Delete, is not
// touched. a.mu must be held.
func (a *Accounts) undo(name string, res reservation) {
	f, w := res.f, res.w
	if f == nil || a.failures[name] != f {
		return
	}
	if w.count > 0 {
		w.count--
	}
	if l := f.win; l.before == w && l.count == 0 && w.count < a.policy.LockoutThreshold {
		f.win, f.lockedUntil = w, time.Time{}
	}
}

// clearWindow is called after a login with the right password: it takes the
// login's failure back, and forgets the failures of the user name if the
// window the login was counted in is still the current one, not replaced by
// a reset or a lock that stands. a.mu must be held.
func (a *Accounts) clearWindow(name string, res reservation) {
	a.undo(name, res)
	if f := res.f; f != nil && a.failures[name] == f && res.w == f.win {
		delete(a.failures, name)
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
	if iter < 1 || iter > maxIterations {
		return "", fmt.Errorf("auth: %d PBKDF2 iterations, want 1 to %d", iter, maxIterations)
	}
	salt := randomBytes(16)
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$pbkdf2-sha256$%d$%s$%s", iter, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// pbkdf2Hash is a stored password hash, split.
type pbkdf2Hash struct {
	iter      int
	salt, key []byte
}

// parseHash splits a stored hash, and reports whether it is one that can be
// checked. The zero pbkdf2Hash, of a hash that cannot, matches no password.
func parseHash(h string) (pbkdf2Hash, bool) {
	f := strings.Split(h, "$")
	if len(f) != 5 || f[0] != "" || f[1] != "pbkdf2-sha256" {
		return pbkdf2Hash{}, false
	}
	iter, err := strconv.Atoi(f[2])
	if err != nil || iter < 1 || iter > maxIterations {
		return pbkdf2Hash{}, false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(f[3])
	key, err2 := b64.DecodeString(f[4])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(salt) > 64 || len(key) < 16 || len(key) > 64 {
		return pbkdf2Hash{}, false
	}
	return pbkdf2Hash{iter, salt, key}, true
}

// hashIterations returns the iteration count of a stored hash, 0 if it has
// none.
func hashIterations(h string) int {
	p, _ := parseHash(h)
	return p.iter
}

// work returns the PBKDF2 iterations that checking the hash takes: its count
// for each SHA-256 block of its key.
func (p pbkdf2Hash) work() int {
	return p.iter * ((len(p.key) + sha256.Size - 1) / sha256.Size)
}

// matches reports whether password matches the hash.
func (p pbkdf2Hash) matches(password string) bool {
	if p.iter == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, p.salt, p.iter, len(p.key))
	return err == nil && subtle.ConstantTimeCompare(got, p.key) == 1
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
		// The line itself is left out of errors: it may hold a hash.
		bad := func(what string) error { return fmt.Errorf("auth: account database line %d: %s", n, what) }
		switch {
		case f[0] == "policy" && len(f) == 3:
			if f[1] == "HTTPBasicAuth" {
				p.BasicAuth = BasicAuth(f[2])
				continue
			}
			v, err := strconv.Atoi(f[2])
			if err != nil || v < 0 || v > maxSetting {
				return nil, p, bad("bad value of " + f[1])
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
				return nil, p, bad("unknown setting")
			}
		case f[0] == "account" && len(f) == 5:
			acc := &account{Account: Account{UserName: f[1], RoleId: f[2], Enabled: f[3] == "enabled", gen: newGen()}, hash: f[4]}
			_, hashOK := parseHash(acc.hash)
			switch {
			case checkName(acc.UserName) != nil:
				return nil, p, bad("bad user name")
			case privileges(acc.RoleId) == nil:
				return nil, p, bad("unknown role")
			case f[3] != "enabled" && f[3] != "disabled":
				return nil, p, bad("bad state")
			case !hashOK:
				return nil, p, bad("bad password hash")
			case slices.ContainsFunc(list, func(o *account) bool { return o.UserName == acc.UserName }):
				return nil, p, bad("duplicate user name")
			}
			list = append(list, acc)
		default:
			return nil, p, bad("unknown entry")
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
