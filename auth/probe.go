package auth

import "sync/atomic"

// probe lets tests watch and pause the hashing of passwords and the changes
// of accounts. Accounts and Service have none outside tests; its methods do
// nothing on a nil probe.
type probe struct {
	hashed  atomic.Int64 // passwords checked
	work    atomic.Int64 // PBKDF2 iterations of logins, new hashes included
	active  atomic.Int32 // passwords being checked
	maxSeen atomic.Int32 // most passwords checked at once

	// beforeCheck is called by a login that holds a slot and has chosen the
	// hash to check, beforeChange by change after hashing a new password,
	// if any, before it takes the lock, and beforeRebind first by
	// keepOwnSession.
	beforeCheck, beforeChange, beforeRebind func()
}

func (p *probe) check() {
	if p != nil && p.beforeCheck != nil {
		p.beforeCheck()
	}
}

func (p *probe) change() {
	if p != nil && p.beforeChange != nil {
		p.beforeChange()
	}
}

func (p *probe) rebind() {
	if p != nil && p.beforeRebind != nil {
		p.beforeRebind()
	}
}

func (p *probe) addWork(n int) {
	if p != nil {
		p.work.Add(int64(n))
	}
}

// hashing counts a password check of work iterations, until the returned
// function is called.
func (p *probe) hashing(work int) func() {
	p.hashed.Add(1)
	p.addWork(work)
	n := p.active.Add(1)
	for m := p.maxSeen.Load(); n > m && !p.maxSeen.CompareAndSwap(m, n); m = p.maxSeen.Load() {
	}
	return func() { p.active.Add(-1) }
}
