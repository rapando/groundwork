package auth

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	ceremonyTTL   = 5 * time.Minute
	maxCeremonies = 64
	maxNameLen    = 64
)

var ErrCeremony = errors.New("the passkey request expired or was already used; try again")

// passkeys runs WebAuthn ceremonies. Each begins with options for the
// browser and ends with the browser's signed answer; the challenge between
// the two is kept here under a random ID the browser sends back.
type passkeys struct {
	wa *webauthn.WebAuthn

	mu         sync.Mutex
	ceremonies map[string]ceremony
}

type ceremony struct {
	data     webauthn.SessionData
	register bool
	expires  time.Time
}

func newPasskeys(origin string) (*passkeys, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "groundwork",
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, err
	}
	return &passkeys{wa: wa, ceremonies: map[string]ceremony{}}, nil
}

func (p *passkeys) put(c ceremony, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, old := range p.ceremonies {
		if now.After(old.expires) {
			delete(p.ceremonies, id)
		}
	}
	if len(p.ceremonies) >= maxCeremonies { // abandoned prompts; the oldest matter least
		var oldest string
		for id, old := range p.ceremonies {
			if oldest == "" || old.expires.Before(p.ceremonies[oldest].expires) {
				oldest = id
			}
		}
		delete(p.ceremonies, oldest)
	}
	id := hex.EncodeToString(randomBytes(16))
	c.expires = now.Add(ceremonyTTL)
	p.ceremonies[id] = c
	return id
}

// take removes and returns a ceremony: each challenge is answered once.
func (p *passkeys) take(id string, register bool, now time.Time) (ceremony, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.ceremonies[id]
	delete(p.ceremonies, id)
	if !ok || c.register != register || now.After(c.expires) {
		return ceremony{}, ErrCeremony
	}
	return c, nil
}

// waUser adapts the stored state to go-webauthn's single user.
type waUser struct{ st *state }

func (u waUser) WebAuthnID() []byte          { return u.st.UserID }
func (u waUser) WebAuthnName() string        { return accountName() }
func (u waUser) WebAuthnDisplayName() string { return accountName() }
func (u waUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.st.Passkeys))
	for _, p := range u.st.Passkeys {
		var c webauthn.Credential
		if json.Unmarshal(p.Credential, &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// accountName labels the passkey in the OS's password manager.
func accountName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username + " (groundwork)"
	}
	return "groundwork"
}

// BeginRegistration starts adding a passkey. Without a signed-in session it
// is only allowed while nothing is set up (first-run setup).
func (a *Auth) BeginRegistration(signedIn bool) (*protocol.CredentialCreation, string, error) {
	if a.pk == nil {
		return nil, "", ErrNoPasskeys
	}
	a.mu.Lock()
	st, err := a.load()
	if err == nil && len(st.UserID) == 0 {
		cp := *st
		err = a.save(&cp) // gives the account its user handle before the first passkey
		st = a.st
	}
	a.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	if !signedIn && st.configured() {
		return nil, "", ErrConfigured
	}
	u := waUser{st}
	var exclude []protocol.CredentialDescriptor
	for _, c := range u.WebAuthnCredentials() {
		exclude = append(exclude, c.Descriptor())
	}
	creation, data, err := a.pk.wa.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired, // the fingerprint or device PIN, not just presence
		}),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		return nil, "", err
	}
	return creation, a.pk.put(ceremony{data: *data, register: true}, a.now()), nil
}

// FinishRegistration verifies the browser's answer in r's body and stores the passkey.
func (a *Auth) FinishRegistration(id, name string, signedIn bool, r *http.Request) error {
	if a.pk == nil {
		return ErrNoPasskeys
	}
	c, err := a.pk.take(id, true, a.now())
	if err != nil {
		return err
	}
	st, err := a.snapshot()
	if err != nil {
		return err
	}
	if !signedIn && st.configured() {
		return ErrConfigured
	}
	cred, err := a.pk.wa.FinishRegistration(waUser{st}, c.data, r)
	if err != nil {
		return passkeyError(err)
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	name = cleanName(name)
	return a.update(func(st *state) error {
		if !signedIn && st.configured() {
			return ErrConfigured
		}
		if name == "" {
			name = "Passkey " + strconv.Itoa(len(st.Passkeys)+1)
		}
		st.Passkeys = append(st.Passkeys, Passkey{
			ID: base64.RawURLEncoding.EncodeToString(cred.ID), Name: name, Created: a.now().UTC(), Credential: raw,
		})
		return nil
	})
}

// BeginLogin asks the browser for a signature from one of the stored passkeys.
func (a *Auth) BeginLogin() (*protocol.CredentialAssertion, string, error) {
	if a.pk == nil {
		return nil, "", ErrNoPasskeys
	}
	st, err := a.snapshot()
	if err != nil {
		return nil, "", err
	}
	if len(st.Passkeys) == 0 {
		return nil, "", errors.New("no passkeys are set up")
	}
	assertion, data, err := a.pk.wa.BeginLogin(waUser{st}, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	return assertion, a.pk.put(ceremony{data: *data}, a.now()), nil
}

// FinishLogin verifies the signature in r's body and records the passkey's use.
func (a *Auth) FinishLogin(id string, r *http.Request) error {
	if a.pk == nil {
		return ErrNoPasskeys
	}
	c, err := a.pk.take(id, false, a.now())
	if err != nil {
		return err
	}
	st, err := a.snapshot()
	if err != nil {
		return err
	}
	cred, err := a.pk.wa.FinishLogin(waUser{st}, c.data, r)
	if err != nil {
		return passkeyError(err)
	}
	if cred.Authenticator.CloneWarning {
		return errors.New("this passkey's signature counter went backwards, which suggests a cloned authenticator; remove it and add it again")
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	return a.update(func(st *state) error {
		for i := range st.Passkeys {
			if st.Passkeys[i].ID == credID {
				st.Passkeys[i].Credential, st.Passkeys[i].LastUsed = raw, a.now().UTC()
				return nil
			}
		}
		return ErrBadCredentials // removed while the prompt was open
	})
}

// passkeyError keeps go-webauthn's detail (useful in the service log) out
// of the answer the browser sees.
type PasskeyError struct{ Err error }

func (e *PasskeyError) Error() string { return "the passkey could not be verified" }
func (e *PasskeyError) Unwrap() error { return e.Err }

func passkeyError(err error) error {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		err = errors.New(pe.Details + ": " + pe.DevInfo)
	}
	return &PasskeyError{Err: err}
}

func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for len(s) > maxNameLen || !utf8.ValidString(s) {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-max(n, 1)]
	}
	return s
}
