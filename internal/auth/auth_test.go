package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
)

const origin = "http://localhost:7420"

func newAuth(t *testing.T) (*Auth, string) {
	t.Helper()
	home := t.TempDir()
	a, err := New(home, origin)
	if err != nil {
		t.Fatal(err)
	}
	return a, home
}

func TestPasswordSetupLoginAndChange(t *testing.T) {
	a, home := newAuth(t)
	if a.Configured() {
		t.Fatal("a fresh home is not configured")
	}
	if _, _, err := a.NewSession(); err == nil {
		t.Fatal("no session before sign-in is set up")
	}
	var input InputError
	if err := a.SetupPassword("short"); !errors.As(err, &input) {
		t.Fatalf("short password: %v", err)
	}
	if err := a.SetupPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetupPassword("another one"); !errors.Is(err, ErrConfigured) {
		t.Fatalf("second setup: %v", err)
	}
	if fi, _ := os.Stat(Path(home)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode %v", fi.Mode())
	}
	if b, _ := os.ReadFile(Path(home)); bytes.Contains(b, []byte("correct horse")) || !bytes.Contains(b, []byte("$argon2id$")) {
		t.Fatalf("the password must be stored hashed: %s", b)
	}
	if err := a.CheckPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := a.CheckPassword("Correct horse"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := a.ChangePassword("nope", "battery staple"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("change without the current password: %v", err)
	}
	if err := a.ChangePassword("correct horse", "battery staple"); err != nil {
		t.Fatal(err)
	}
	if a.CheckPassword("battery staple") != nil || a.CheckPassword("correct horse") == nil {
		t.Fatal("the new password replaces the old")
	}
	if err := a.RemovePassword(); !errors.Is(err, ErrLastMethod) {
		t.Fatalf("removing the only method: %v", err)
	}
}

func TestWrongPasswordsBackOff(t *testing.T) {
	a, _ := newAuth(t)
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	if err := a.SetupPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := a.CheckPassword("guess"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var tooMany *TooManyError
	if err := a.CheckPassword("correct horse"); !errors.As(err, &tooMany) || tooMany.RetryAfter != time.Second {
		t.Fatalf("after five failures even the right password waits: %v", err)
	}
	now = now.Add(time.Second)
	if err := a.CheckPassword("correct horse"); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	if failureDelay(4) != 0 || failureDelay(6) != 2*time.Second || failureDelay(50) != time.Minute {
		t.Fatal("delays: free, then doubling, capped at a minute")
	}
}

func TestSessions(t *testing.T) {
	a, home := newAuth(t)
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	if err := a.SetupPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	v, exp, err := a.NewSession()
	if err != nil || !a.ValidSession(v) || !exp.Equal(now.Add(SessionTTL)) {
		t.Fatalf("%q %v %v", v, exp, err)
	}
	for _, bad := range []string{"", "x", v + "x", "AAAAAAAAAAA." + strings.Split(v, ".")[1], strings.Repeat("a", 200)} {
		if a.ValidSession(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	now = now.Add(SessionTTL)
	if a.ValidSession(v) {
		t.Fatal("expired session accepted")
	}
	now = now.Add(-time.Hour)
	if !a.ValidSession(v) {
		t.Fatal("unexpired session rejected")
	}

	// signing out everywhere rotates the key
	if err := a.SignOutEverywhere(); err != nil {
		t.Fatal(err)
	}
	if a.ValidSession(v) {
		t.Fatal("old session survived sign-out-everywhere")
	}

	// reset (the CLI deletes the file) takes effect in the running service
	v, _, _ = a.NewSession()
	if err := Reset(home); err != nil {
		t.Fatal(err)
	}
	if a.ValidSession(v) || a.Configured() {
		t.Fatal("reset must sign everyone out and return to setup")
	}
	if err := Reset(home); err != nil {
		t.Fatalf("resetting twice: %v", err)
	}

	// another Auth on the same home (a restart) honours sessions
	if err := a.SetupPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	v, _, _ = a.NewSession()
	b, err := New(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if !b.ValidSession(v) {
		t.Fatal("sessions survive a restart")
	}
}

func TestCorruptFileNamesTheWayOut(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(Path(home), []byte("{"), 0o600)
	if _, err := New(home, ""); err == nil || !strings.Contains(err.Error(), "groundwork auth reset") {
		t.Fatalf("%v", err)
	}
}

func TestCleanName(t *testing.T) {
	if got := cleanName("  Touch  ID\n on   Mac "); got != "Touch ID on Mac" {
		t.Fatal(got)
	}
	if got := cleanName(strings.Repeat("é", 100)); len(got) > maxNameLen || !strings.HasPrefix(got, "éé") {
		t.Fatalf("%q", got)
	}
}

// --- passkeys, with a software authenticator ---

type authenticator struct {
	key   *ecdsa.PrivateKey
	id    []byte
	count uint32
}

func newAuthenticator(t *testing.T) *authenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &authenticator{key: k, id: id}
}

var b64 = base64.RawURLEncoding

func clientData(typ, challenge, org string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": org, "crossOrigin": false})
	return b
}

func (k *authenticator) authData(attested bool) []byte {
	rp := sha256.Sum256([]byte("localhost"))
	var buf bytes.Buffer
	buf.Write(rp[:])
	flags := byte(0x01 | 0x04) // user present, user verified
	if attested {
		flags |= 0x40
	}
	buf.WriteByte(flags)
	k.count++
	binary.Write(&buf, binary.BigEndian, k.count)
	if attested {
		buf.Write(make([]byte, 16)) // AAGUID
		binary.Write(&buf, binary.BigEndian, uint16(len(k.id)))
		buf.Write(k.id)
		x, y := make([]byte, 32), make([]byte, 32)
		k.key.X.FillBytes(x)
		k.key.Y.FillBytes(y)
		cose, _ := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		buf.Write(cose)
	}
	return buf.Bytes()
}

func (k *authenticator) create(challenge, org string) []byte {
	att, _ := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(true)})
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", challenge, org)), "attestationObject": b64.EncodeToString(att)},
	})
	return b
}

func (k *authenticator) get(challenge, org string, userHandle []byte) []byte {
	cd := clientData("webauthn.get", challenge, org)
	ad := k.authData(false)
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(userHandle)},
	})
	return b
}

func post(body []byte) *http.Request {
	return httptest.NewRequest("POST", "/", bytes.NewReader(body))
}

func register(t *testing.T, a *Auth, k *authenticator, name string, signedIn bool) error {
	t.Helper()
	creation, id, err := a.BeginRegistration(signedIn)
	if err != nil {
		return err
	}
	return a.FinishRegistration(id, name, signedIn, post(k.create(creation.Response.Challenge.String(), origin)))
}

func login(t *testing.T, a *Auth, k *authenticator, org string) error {
	t.Helper()
	assertion, id, err := a.BeginLogin()
	if err != nil {
		return err
	}
	return a.FinishLogin(id, post(k.get(assertion.Response.Challenge.String(), org, a.st.UserID)))
}

func TestPasskeySetupAndLogin(t *testing.T) {
	a, _ := newAuth(t)
	k := newAuthenticator(t)
	if err := register(t, a, k, "Touch ID", false); err != nil {
		t.Fatalf("first-run passkey: %v", err)
	}
	st, _ := a.Status()
	if !st.Configured || st.Password || len(st.Passkeys) != 1 || st.Passkeys[0].Name != "Touch ID" {
		t.Fatalf("%+v", st)
	}
	// set up now: a signed-out browser can't add another
	if _, _, err := a.BeginRegistration(false); !errors.Is(err, ErrConfigured) {
		t.Fatalf("signed-out registration after setup: %v", err)
	}
	if err := login(t, a, k, origin); err != nil {
		t.Fatalf("login: %v", err)
	}
	if st, _ := a.Status(); st.Passkeys[0].LastUsed.IsZero() {
		t.Fatal("last use recorded")
	}
	// a signature for another origin (a phishing page) fails
	var pe *PasskeyError
	if err := login(t, a, k, "http://localhost:9999"); !errors.As(err, &pe) {
		t.Fatalf("foreign origin: %v", err)
	}
	// another authenticator isn't registered
	if err := login(t, a, newAuthenticator(t), origin); err == nil {
		t.Fatal("unknown passkey accepted")
	}
	// a ceremony is answered once
	assertion, id, _ := a.BeginLogin()
	body := k.get(assertion.Response.Challenge.String(), origin, a.st.UserID)
	if err := a.FinishLogin(id, post(body)); err != nil {
		t.Fatal(err)
	}
	if err := a.FinishLogin(id, post(body)); !errors.Is(err, ErrCeremony) {
		t.Fatalf("replay: %v", err)
	}

	// a second passkey, signed in; then the first can go, but not the last
	k2 := newAuthenticator(t)
	if err := register(t, a, k2, "", true); err != nil {
		t.Fatal(err)
	}
	st, _ = a.Status()
	if len(st.Passkeys) != 2 || st.Passkeys[1].Name != "Passkey 2" {
		t.Fatalf("%+v", st.Passkeys)
	}
	if err := a.RemovePasskey(st.Passkeys[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.RemovePasskey(st.Passkeys[1].ID); !errors.Is(err, ErrLastMethod) {
		t.Fatalf("last passkey: %v", err)
	}
	if err := a.RemovePasskey("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := login(t, a, k, origin); err == nil {
		t.Fatal("a removed passkey still signs in")
	}
	if err := login(t, a, k2, origin); err != nil {
		t.Fatal(err)
	}
}

func TestPasskeysOffWithoutOrigin(t *testing.T) {
	a, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if a.PasskeysAvailable() {
		t.Fatal("no origin, no passkeys")
	}
	if _, _, err := a.BeginRegistration(false); !errors.Is(err, ErrNoPasskeys) {
		t.Fatal(err)
	}
}
