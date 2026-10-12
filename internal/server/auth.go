package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/auth"
)

const (
	ceremonyHeader = "X-Groundwork-Ceremony"
	maxAuthBody    = 64 << 10
)

// authRoutes are reachable signed out: they are how a browser signs in, or
// sets sign-in up on first run. State-changing ones still pass the Origin
// check, so another site can't drive them.
func (s *Server) authRoutes(r chi.Router) {
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if s.auth == nil {
				writeError(w, http.StatusServiceUnavailable, "no_auth", "browser sign-in is not available")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/status", s.authStatus)
	r.Post("/password/setup", s.passwordSetup)
	r.Post("/password/login", s.passwordLogin)
	r.Post("/passkeys/register/begin", s.passkeyRegisterBegin)
	r.Post("/passkeys/register/finish", s.passkeyRegisterFinish)
	r.Post("/passkeys/login/begin", s.passkeyLoginBegin)
	r.Post("/passkeys/login/finish", s.passkeyLoginFinish)
	r.Post("/sign-out", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]any{})
	})
}

// accountRoutes manage sign-in methods; they sit behind the API's session
// and token checks.
func (s *Server) accountRoutes(r chi.Router) {
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.auth == nil {
				writeError(w, http.StatusServiceUnavailable, "no_auth", "browser sign-in is not available")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		st, err := s.auth.Status()
		if err != nil {
			s.authError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"password": st.Password, "passkeys": st.Passkeys, "passkeys_available": s.auth.PasskeysAvailable()})
	})
	r.Put("/password", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Current, Password string }
		if !decodeAuth(w, r, &body) {
			return
		}
		if err := s.auth.ChangePassword(body.Current, body.Password); err != nil {
			s.authError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{})
	})
	r.Delete("/password", func(w http.ResponseWriter, _ *http.Request) {
		if err := s.auth.RemovePassword(); err != nil {
			s.authError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{})
	})
	r.Delete("/passkeys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.auth.RemovePasskey(chi.URLParam(r, "id")); err != nil {
			s.authError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{})
	})
	// signs every other browser out: a new session key, and a new cookie for this one
	r.Post("/sign-out-others", func(w http.ResponseWriter, _ *http.Request) {
		if err := s.auth.SignOutEverywhere(); err != nil {
			s.authError(w, err)
			return
		}
		s.startSession(w)
	})
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.auth.Status()
	if err != nil {
		s.authError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"configured":         st.Configured,
		"password":           st.Password,
		"passkeys":           len(st.Passkeys) > 0,
		"passkeys_available": s.auth.PasskeysAvailable() && s.onCanonicalHost(r),
		"signed_in":          s.signedIn(r),
		// first-run setup is for someone at this machine
		"setup_allowed": !st.Configured && fromLoopback(r),
	})
}

func (s *Server) passwordSetup(w http.ResponseWriter, r *http.Request) {
	if !fromLoopback(r) {
		writeError(w, http.StatusForbidden, "not_local", "set sign-in up from the machine groundwork runs on")
		return
	}
	var body struct{ Password string }
	if !decodeAuth(w, r, &body) {
		return
	}
	if err := s.auth.SetupPassword(body.Password); err != nil {
		s.authError(w, err)
		return
	}
	s.log.Info("sign-in set up", "method", "password")
	s.startSession(w)
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{ Password string }
	if !decodeAuth(w, r, &body) {
		return
	}
	if err := s.auth.CheckPassword(body.Password); err != nil {
		s.log.Warn("sign-in failed", "method", "password", "remote", r.RemoteAddr)
		s.authError(w, err)
		return
	}
	s.startSession(w)
}

// canRegister: a signed-in browser adds passkeys; signed out, only the
// first-run setup from this machine may.
func (s *Server) canRegister(w http.ResponseWriter, r *http.Request) (signedIn, ok bool) {
	signedIn = s.signedIn(r)
	if !signedIn && !fromLoopback(r) {
		writeError(w, http.StatusForbidden, "not_local", "set sign-in up from the machine groundwork runs on")
		return false, false
	}
	return signedIn, true
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	signedIn, ok := s.canRegister(w, r)
	if !ok {
		return
	}
	creation, id, err := s.auth.BeginRegistration(signedIn)
	if err != nil {
		s.authError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ceremony": id, "options": creation})
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	signedIn, ok := s.canRegister(w, r)
	if !ok {
		return
	}
	if err := s.auth.FinishRegistration(r.Header.Get(ceremonyHeader), r.URL.Query().Get("name"), signedIn, r); err != nil {
		s.authError(w, err)
		return
	}
	s.log.Info("passkey added")
	if signedIn {
		writeJSON(w, 200, map[string]any{})
		return
	}
	s.startSession(w) // first-run setup signs this browser in
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, _ *http.Request) {
	assertion, id, err := s.auth.BeginLogin()
	if err != nil {
		s.authError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ceremony": id, "options": assertion})
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.FinishLogin(r.Header.Get(ceremonyHeader), r); err != nil {
		s.log.Warn("sign-in failed", "method", "passkey", "remote", r.RemoteAddr)
		s.authError(w, err)
		return
	}
	s.startSession(w)
}

// startSession sets a fresh signed session cookie.
func (s *Server) startSession(w http.ResponseWriter) {
	v, exp, err := s.auth.NewSession()
	if err != nil {
		s.authError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: v, Path: "/", Expires: exp, MaxAge: int(time.Until(exp).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, 200, map[string]any{"signed_in": true})
}

func (s *Server) authError(w http.ResponseWriter, err error) {
	var tooMany *auth.TooManyError
	var input auth.InputError
	var pk *auth.PasskeyError
	switch {
	case errors.As(err, &tooMany):
		w.Header().Set("Retry-After", strconv.Itoa(int(tooMany.RetryAfter.Seconds()+0.999)))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", err.Error())
	case errors.Is(err, auth.ErrBadCredentials):
		writeError(w, http.StatusUnauthorized, "bad_credentials", err.Error())
	case errors.Is(err, auth.ErrConfigured):
		writeError(w, http.StatusConflict, "configured", "sign-in is already set up; sign in first")
	case errors.Is(err, auth.ErrLastMethod):
		writeError(w, http.StatusConflict, "last_method", err.Error())
	case errors.As(err, &input):
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	case errors.As(err, &pk):
		s.log.Warn("passkey rejected", "err", pk.Err)
		writeError(w, http.StatusBadRequest, "passkey_failed", err.Error())
	case errors.Is(err, auth.ErrNoPasskeys), errors.Is(err, auth.ErrCeremony):
		writeError(w, http.StatusBadRequest, "passkey_unavailable", err.Error())
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "not_found", "no such passkey")
	default:
		s.log.Error("sign-in error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "sign-in failed; see the service log")
	}
}

func decodeAuth(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "invalid request body")
		return false
	}
	return true
}

// fromLoopback reports whether the client connected from this machine.
func fromLoopback(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// onCanonicalHost: passkeys work only on the origin they are bound to.
func (s *Server) onCanonicalHost(r *http.Request) bool {
	return s.canonical != "" && r.Host == s.canonical
}
