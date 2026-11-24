package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultMaxSessions is the number of open sessions when Service.MaxSessions
// is zero. One account may have a quarter of them; its oldest session ends
// when it opens more.
const DefaultMaxSessions = 64

// session is an open Redfish session. Only the hash of its token is kept.
type session struct {
	id       string
	user     string
	gen      uint64 // of the account when the session was opened
	origin   string
	created  time.Time
	lastUsed time.Time
	holds    int // requests changing the password of the session's own account
}

// sessions holds the open sessions. A session ends when it has not been used
// for the timeout of the policy.
type sessions struct {
	mu      sync.Mutex
	byID    map[string]*session
	byToken map[[32]byte]*session
	tokens  map[string][32]byte // token hash by session id
}

func newSessions() *sessions {
	return &sessions{
		byID:    map[string]*session{},
		byToken: map[[32]byte]*session{},
		tokens:  map[string][32]byte{},
	}
}

// create opens a session of acc and returns it with its token, or false when
// limit sessions are open. When acc has limit/4 sessions, its oldest one
// ends. When limit sessions are open, those that current reports as ended,
// as their account changed, are dropped first, except those held by a
// change of their own password (hold). current is called with s.mu held, so
// it must not use the sessions.
func (s *sessions) create(acc Account, origin string, now time.Time, timeout time.Duration, limit int, current func(user string, gen uint64) bool) (session, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now, timeout)
	if len(s.byID) >= limit {
		for id, sess := range s.byID {
			if sess.holds == 0 && !current(sess.user, sess.gen) {
				s.drop(id)
			}
		}
	}
	var own []*session
	for _, sess := range s.byID {
		if sess.user == acc.UserName {
			own = append(own, sess)
		}
	}
	if len(own) >= max(limit/4, 1) {
		oldest := slices.MinFunc(own, func(a, b *session) int { return a.created.Compare(b.created) })
		s.drop(oldest.id)
	}
	if len(s.byID) >= limit {
		return session{}, "", false
	}
	id := make([]byte, 8)
	tok := make([]byte, 32)
	rand.Read(id)
	rand.Read(tok)
	token := base64.RawURLEncoding.EncodeToString(tok)
	sess := &session{id: hex.EncodeToString(id), user: acc.UserName, gen: acc.gen, origin: origin, created: now, lastUsed: now}
	h := sha256.Sum256([]byte(token))
	s.byID[sess.id] = sess
	s.byToken[h] = sess
	s.tokens[sess.id] = h
	return *sess, token, true
}

// lookup returns the session of token.
func (s *sessions) lookup(token string, now time.Time, timeout time.Duration) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now, timeout)
	sess := s.byToken[sha256.Sum256([]byte(token))]
	if sess == nil {
		return session{}, false
	}
	return *sess, true
}

// touch marks the session with the id as used now.
func (s *sessions) touch(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.byID[id]; sess != nil {
		sess.lastUsed = now
	}
}

// get returns the session with the id.
func (s *sessions) get(id string, now time.Time, timeout time.Duration) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now, timeout)
	sess := s.byID[id]
	if sess == nil {
		return session{}, false
	}
	return *sess, true
}

// list returns the open sessions, oldest first.
func (s *sessions) list(now time.Time, timeout time.Duration) []session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now, timeout)
	out := make([]session, 0, len(s.byID))
	for _, sess := range s.byID {
		out = append(out, *sess)
	}
	slices.SortFunc(out, func(a, b session) int {
		if c := a.created.Compare(b.created); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	return out
}

// hold keeps the session with the id from being dropped as ended while it
// changes the password of its own account, until the returned function is
// called: between the change and rebind, its generation is an old one.
func (s *sessions) hold(id string) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byID[id]
	if sess == nil {
		return func() {}
	}
	sess.holds++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sess.holds--
	}
}

// rebind moves the session with the id from generation from of its account
// to generation to, for the session that gave its own account a new
// password.
func (s *sessions) rebind(id string, from, to uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.byID[id]; sess != nil && sess.gen == from {
		sess.gen = to
	}
}

// remove ends the session with the id, and reports whether it was open.
func (s *sessions) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[id] == nil {
		return false
	}
	s.drop(id)
	return true
}

// removeUser ends the sessions of user except the one with the id keep.
func (s *sessions) removeUser(user, keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.byID {
		if sess.user == user && id != keep {
			s.drop(id)
		}
	}
}

// expire ends the sessions not used for timeout. s.mu must be held.
func (s *sessions) expire(now time.Time, timeout time.Duration) {
	for id, sess := range s.byID {
		if now.Sub(sess.lastUsed) >= timeout {
			s.drop(id)
		}
	}
}

// drop removes the session with the id. s.mu must be held.
func (s *sessions) drop(id string) {
	delete(s.byToken, s.tokens[id])
	delete(s.tokens, id)
	delete(s.byID, id)
}
