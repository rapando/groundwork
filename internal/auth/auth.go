// Package auth holds the ways a browser signs in to the console (a password
// and passkeys: Touch ID, Windows Hello, a security key) and the signed
// session cookies they grant.
//
// Everything lives in <home>/auth.json (mode 0600). The file is re-read when
// it changes, so `groundwork auth reset` (which deletes it) takes effect in a
// running service at once: with the file gone there are no credentials and
// no session key, so every browser is signed out and the next visit sets
// sign-in up again.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	fileName    = "auth.json"
	format      = 1
	MinPassword = 8
	maxPassword = 1024
	// SessionTTL is how long a sign-in lasts in one browser.
	SessionTTL = 7 * 24 * time.Hour
)

var (
	ErrBadCredentials = errors.New("wrong password")
	ErrConfigured     = errors.New("sign-in is already set up")
	ErrLastMethod     = errors.New("this is the only way to sign in; add another before removing it")
	ErrNoPasskeys     = errors.New("passkeys are only available at http://localhost")
)

// InputError is a new password or name the user has to change.
type InputError string

func (e InputError) Error() string { return string(e) }

// TooManyError is a password attempt refused because of earlier failures.
type TooManyError struct{ RetryAfter time.Duration }

func (e *TooManyError) Error() string {
	return fmt.Sprintf("too many wrong passwords; try again in %d s", int(e.RetryAfter.Seconds()+0.999))
}

// Passkey is one registered WebAuthn credential.
type Passkey struct {
	ID       string    `json:"id"` // base64url credential ID
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
	// Credential is the go-webauthn record (public key, sign count, flags).
	Credential json.RawMessage `json:"credential"`
}

type state struct {
	Format     int       `json:"format"`
	UserID     []byte    `json:"user_id"`
	SessionKey []byte    `json:"session_key"`
	Password   string    `json:"password,omitempty"` // argon2id, PHC string
	Passkeys   []Passkey `json:"passkeys,omitempty"`
}

func (s *state) configured() bool { return s.Password != "" || len(s.Passkeys) > 0 }

// Status is what the console shows about sign-in; it never carries secrets.
type Status struct {
	Configured bool          `json:"configured"`
	Password   bool          `json:"password"`
	Passkeys   []PasskeyInfo `json:"passkeys"`
}

type PasskeyInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

// Auth is safe for concurrent use.
type Auth struct {
	path string
	now  func() time.Time
	pk   *passkeys // nil when passkeys can't work (no localhost origin)

	mu      sync.Mutex
	st      *state
	modTime time.Time
	size    int64

	fails   int // consecutive wrong passwords
	nextTry time.Time
}

// New opens the sign-in state in home. origin is the console's
// http://localhost:<port> address, which passkeys are bound to; "" turns
// passkeys off.
func New(home, origin string) (*Auth, error) {
	a := &Auth{path: filepath.Join(home, fileName), now: time.Now}
	if origin != "" {
		pk, err := newPasskeys(origin)
		if err != nil {
			return nil, err
		}
		a.pk = pk
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

// Reset removes every credential from home and signs every browser out.
func Reset(home string) error {
	err := os.Remove(filepath.Join(home, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Path is the file the state lives in.
func Path(home string) string { return filepath.Join(home, fileName) }

// load returns the current state, re-reading the file when it changed. A
// missing file is the empty (not configured) state. Callers hold a.mu.
func (a *Auth) load() (*state, error) {
	fi, err := os.Stat(a.path)
	if errors.Is(err, os.ErrNotExist) {
		if a.st == nil || a.st.configured() || a.size != 0 {
			a.st, a.modTime, a.size = &state{Format: format}, time.Time{}, 0
		}
		return a.st, nil
	}
	if err != nil {
		return nil, err
	}
	if a.st != nil && fi.ModTime().Equal(a.modTime) && fi.Size() == a.size {
		return a.st, nil
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w (run `groundwork auth reset` to start over)", a.path, err)
	}
	if st.Format > format {
		return nil, fmt.Errorf("%s was written by a newer groundwork (format %d)", a.path, st.Format)
	}
	a.st, a.modTime, a.size = &st, fi.ModTime(), fi.Size()
	return a.st, nil
}

// save writes st atomically. Callers hold a.mu.
func (a *Auth) save(st *state) error {
	if len(st.UserID) == 0 {
		st.UserID = randomBytes(32)
	}
	if len(st.SessionKey) == 0 {
		st.SessionKey = randomBytes(32)
	}
	st.Format = format
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	a.st = st
	if fi, err := os.Stat(a.path); err == nil {
		a.modTime, a.size = fi.ModTime(), fi.Size()
	}
	return nil
}

// update loads, copies, edits and saves the state in one critical section.
func (a *Auth) update(f func(st *state) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, err := a.load()
	if err != nil {
		return err
	}
	st := *cur
	st.Passkeys = append([]Passkey(nil), cur.Passkeys...)
	if err := f(&st); err != nil {
		return err
	}
	return a.save(&st)
}

func (a *Auth) snapshot() (*state, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.load()
}

// Configured reports whether any sign-in method is set up.
func (a *Auth) Configured() bool {
	st, err := a.snapshot()
	return err == nil && st.configured()
}

// PasskeysAvailable reports whether this service can register and check passkeys.
func (a *Auth) PasskeysAvailable() bool { return a.pk != nil }

func (a *Auth) Status() (Status, error) {
	st, err := a.snapshot()
	if err != nil {
		return Status{}, err
	}
	out := Status{Configured: st.configured(), Password: st.Password != "", Passkeys: []PasskeyInfo{}}
	for _, p := range st.Passkeys {
		out.Passkeys = append(out.Passkeys, PasskeyInfo{ID: p.ID, Name: p.Name, Created: p.Created, LastUsed: p.LastUsed})
	}
	return out, nil
}

func checkNewPassword(pw string) error {
	if len(pw) < MinPassword {
		return InputError(fmt.Sprintf("use at least %d characters", MinPassword))
	}
	if len(pw) > maxPassword {
		return InputError(fmt.Sprintf("use at most %d characters", maxPassword))
	}
	return nil
}

// SetupPassword sets the first password; it fails once anything is set up.
func (a *Auth) SetupPassword(pw string) error {
	if err := checkNewPassword(pw); err != nil {
		return err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	return a.update(func(st *state) error {
		if st.configured() {
			return ErrConfigured
		}
		st.Password = h
		return nil
	})
}

// ChangePassword sets or replaces the password of a signed-in user. When a
// password exists, current must match it.
func (a *Auth) ChangePassword(current, pw string) error {
	if err := checkNewPassword(pw); err != nil {
		return err
	}
	if st, err := a.snapshot(); err != nil {
		return err
	} else if st.Password != "" {
		if err := a.CheckPassword(current); err != nil {
			return err
		}
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	return a.update(func(st *state) error {
		st.Password = h
		return nil
	})
}

// RemovePassword leaves passkeys as the only way in.
func (a *Auth) RemovePassword() error {
	return a.update(func(st *state) error {
		if len(st.Passkeys) == 0 {
			return ErrLastMethod
		}
		st.Password = ""
		return nil
	})
}

// RemovePasskey deletes one passkey by ID.
func (a *Auth) RemovePasskey(id string) error {
	return a.update(func(st *state) error {
		for i, p := range st.Passkeys {
			if p.ID == id {
				if st.Password == "" && len(st.Passkeys) == 1 {
					return ErrLastMethod
				}
				st.Passkeys = append(st.Passkeys[:i], st.Passkeys[i+1:]...)
				return nil
			}
		}
		return os.ErrNotExist
	})
}

// failureDelay is how long to wait after n consecutive wrong passwords: the
// first five are free, then 1 s doubling to a minute.
func failureDelay(n int) time.Duration {
	if n < 5 {
		return 0
	}
	d := time.Second << min(n-5, 6)
	return min(d, time.Minute)
}

// CheckPassword verifies pw, refusing attempts too soon after repeated failures.
func (a *Auth) CheckPassword(pw string) error {
	a.mu.Lock()
	if wait := a.nextTry.Sub(a.now()); wait > 0 {
		a.mu.Unlock()
		return &TooManyError{RetryAfter: wait}
	}
	st, err := a.load()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	ok := st.Password != "" && len(pw) <= maxPassword && verifyPassword(st.Password, pw)
	a.mu.Lock()
	defer a.mu.Unlock()
	if ok {
		a.fails, a.nextTry = 0, time.Time{}
		return nil
	}
	a.fails++
	a.nextTry = a.now().Add(failureDelay(a.fails))
	return ErrBadCredentials
}

// --- sessions ---

const sessionLabel = "groundwork-session-v1"

// NewSession returns a cookie value signed with the current session key,
// valid for SessionTTL.
func (a *Auth) NewSession() (string, time.Time, error) {
	st, err := a.snapshot()
	if err != nil {
		return "", time.Time{}, err
	}
	if !st.configured() || len(st.SessionKey) == 0 {
		return "", time.Time{}, errors.New("sign-in is not set up")
	}
	exp := a.now().Add(SessionTTL).Truncate(time.Second)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(exp.Unix()))
	enc := base64.RawURLEncoding
	return enc.EncodeToString(b[:]) + "." + enc.EncodeToString(sign(st.SessionKey, b[:])), exp, nil
}

// ValidSession reports whether v is an unexpired session signed with the
// current key while sign-in is set up.
func (a *Auth) ValidSession(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	st, err := a.snapshot()
	if err != nil || !st.configured() || len(st.SessionKey) == 0 {
		return false
	}
	enc := base64.RawURLEncoding
	ps, ms, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	p, err := enc.DecodeString(ps)
	if err != nil {
		return false
	}
	m, err := enc.DecodeString(ms)
	if err != nil || len(p) != 8 || !hmac.Equal(m, sign(st.SessionKey, p)) {
		return false
	}
	return a.now().Unix() < int64(binary.BigEndian.Uint64(p))
}

// SignOutEverywhere rotates the session key, so every issued cookie stops working.
func (a *Auth) SignOutEverywhere() error {
	return a.update(func(st *state) error {
		st.SessionKey = randomBytes(32)
		return nil
	})
}

func sign(key, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(sessionLabel))
	h.Write(payload)
	return h.Sum(nil)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return b
}
