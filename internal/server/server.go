// Package server hosts the local HTTP API, SSE stream and embedded SPA.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rapando/groundwork/internal/events"
)

//go:embed all:dist
var distFS embed.FS

const (
	TokenHeader = "X-Groundwork-Token"
	cookieName  = "groundwork_session"
)

// Info is what /api/workspace returns in M0; later milestones extend it.
type Info struct {
	Root    string `json:"root"`
	Version string `json:"version"`
}

type Server struct {
	log   *slog.Logger
	bus   *events.Bus
	info  Info
	token string
	api   func(chi.Router)

	mu    sync.RWMutex
	hosts map[string]bool // allowed Host header values
}

// NewToken returns 32 random bytes, hex encoded.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func New(log *slog.Logger, bus *events.Bus, info Info, token string) *Server {
	return &Server{log: log, bus: bus, info: info, token: token, hosts: map[string]bool{}}
}

// SetAPI installs the function that mounts /api handlers (set before Handler).
func (s *Server) SetAPI(f func(chi.Router)) { s.api = f }

// AllowPort registers the Host header values accepted for the given port.
func (s *Server) AllowPort(port int) {
	p := strconv.Itoa(port)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range []string{"127.0.0.1", "localhost", "[::1]"} {
		s.hosts[h+":"+p] = true
	}
}

// AllowHost registers one extra Host value (used with --host).
func (s *Server) AllowHost(h string) {
	s.mu.Lock()
	s.hosts[h] = true
	s.mu.Unlock()
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(s.requestLog)
	r.Use(securityHeaders)
	r.Use(s.hostCheck)

	// Unauthenticated, leaks nothing but identity; used for single-instance detection.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"app": "groundwork"})
	})

	r.Group(func(r chi.Router) {
		r.Use(s.originCheck)
		r.Use(s.session)

		r.Route("/api", func(r chi.Router) {
			r.Use(s.apiToken)
			if s.api != nil {
				s.api(r)
			}
			r.Get("/events", s.sse)
			r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, 404, "not_found", "no such endpoint")
			})
		})
		r.Handle("/*", s.spa())
	})
	return r
}

// --- middleware ---

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status())
	})
}

// CSP: everything from this origin; no inline or eval'd script. Styles allow
// inline (CodeMirror and Vue set style attributes); images allow data:/blob:
// for the graph's PNG export.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; " +
	"connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// securityHeaders applies to every response, errors included.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer") // the ?t= token must never leave in a Referer
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// hostCheck defeats DNS rebinding: only loopback Host values are served.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		ok := s.hosts[r.Host]
		s.mu.RUnlock()
		if !ok {
			writeError(w, http.StatusForbidden, "bad_host", "host not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originCheck is the CSRF guard: state-changing requests must carry an
// Origin (or Referer) whose host is one we serve.
func (s *Server) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		raw := r.Header.Get("Origin")
		if raw == "" {
			raw = r.Header.Get("Referer")
		}
		if raw == "" || !s.originAllowed(raw) {
			writeError(w, http.StatusForbidden, "bad_origin", "cross-origin request rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(raw string) bool {
	u, err := parseURL(raw)
	if err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return u.Scheme == "http" && s.hosts[u.Host]
}

func (s *Server) tokenEq(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// session exchanges ?t=<token> for an HttpOnly cookie, then requires the
// cookie on everything else.
func (s *Server) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("t"); t != "" && r.Method == http.MethodGet && !isAPI(r) {
			if !s.tokenEq(t) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: s.token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			q := r.URL.Query()
			q.Del("t")
			dest := r.URL.Path
			if enc := q.Encode(); enc != "" {
				dest += "?" + enc
			}
			http.Redirect(w, r, dest, http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !s.tokenEq(c.Value) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid session")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiToken additionally requires the header (read by the SPA from its <meta>
// tag). EventSource cannot set headers, so the SSE stream relies on the
// SameSite=Strict cookie alone.
func (s *Server) apiToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" && !s.tokenEq(r.Header.Get(TokenHeader)) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing token header")
			return
		}
		w.Header().Set("Cache-Control", "no-store") // API data (plans, variables, logs) stays out of caches
		next.ServeHTTP(w, r)
	})
}

func isAPI(r *http.Request) bool {
	return len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api"
}

// --- SSE ---

func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "no_stream", "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	ch, cancel := s.bus.Subscribe()
	defer cancel()
	fmt.Fprint(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	keepalive := newTicker()
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data := ev.Data
			if len(data) == 0 {
				data = []byte("{}")
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			fl.Flush()
		}
	}
}

// --- SPA ---

func (s *Server) spa() http.Handler {
	sub, _ := fs.Sub(distFS, "dist")
	files := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")
	meta := []byte(`<meta name="groundwork-token" content="` + s.token + `">`)
	page := replaceOnce(index, []byte("<!--GROUNDWORK_META-->"), meta)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p != "/" {
			if f, err := sub.Open(p[1:]); err == nil {
				st, _ := f.Stat()
				f.Close()
				if !st.IsDir() {
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		// history-mode fallback
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store") // it carries the session token
		w.Write(page)
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg, "details": nil}})
}

// Listen binds addr, preferring the requested port but falling back to a free one.
func Listen(host string, port int) (net.Listener, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err == nil {
		return l, nil
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}
