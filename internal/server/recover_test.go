package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/server"
)

// A panicking handler answers 500 with a JSON error and is logged through
// the service logger with its stack, not printed to stderr; the server
// keeps serving.
func TestHandlerPanicIsLoggedAndAnswered(t *testing.T) {
	var logs bytes.Buffer
	s := server.New(slog.New(slog.NewTextHandler(&logs, nil)), nil, server.Info{Version: "t"}, token)
	s.SetAPI(func(r chi.Router) {
		r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("handler bug") })
		r.Get("/fine", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	s.AllowHost(strings.TrimPrefix(ts.URL, "http://"))
	get := func(path string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Origin", ts.URL)
		req.Header.Set(server.TokenHeader, token)
		req.AddCookie(&http.Cookie{Name: "groundwork_session", Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	resp, body := get("/api/boom")
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var e struct {
		Error struct{ Code, Message string }
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Code != "internal_error" {
		t.Fatalf("body %s", body)
	}
	if l := logs.String(); !strings.Contains(l, "handler bug") || !strings.Contains(l, "recover_test.go") || !strings.Contains(l, "GET /api/boom") {
		t.Fatalf("log lacks the panic, stack or route: %s", l)
	}
	if resp, _ := get("/api/fine"); resp.StatusCode != 200 {
		t.Fatalf("server stopped serving after a panic: %d", resp.StatusCode)
	}
}
