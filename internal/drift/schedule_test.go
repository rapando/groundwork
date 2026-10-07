package drift

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
)

func rig(t *testing.T, d config.Drift) (*Scheduler, *[]Notice) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "envs/dev"), 0o755)
	cfg := &config.Config{Version: 1, Terraform: config.Terraform{Binary: "terraform", Roots: []config.TFRoot{{Path: "envs/dev", Env: "dev"}}}, Drift: d}
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	t.Cleanup(func() { st.Close() })
	rn := runner.New(root, st, events.NewBus(), func() *config.Config { return cfg }, nil)
	t.Cleanup(func() { rn.Shutdown(context.Background()) })
	fake, _ := filepath.Abs("../../testdata/fake-terraform")
	data, _ := filepath.Abs("../../testdata/terraform")
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DATA", data)
	s := New(rn, st, func() *config.Config { return cfg }, nil)
	t.Cleanup(s.Stop)
	var mu sync.Mutex
	var sent []Notice
	s.Notify = func(_ context.Context, _ config.Drift, n Notice) error {
		mu.Lock()
		sent = append(sent, n)
		mu.Unlock()
		return nil
	}
	return s, &sent
}

func TestScheduledDriftNotifiesOncePerChange(t *testing.T) {
	s, sent := rig(t, config.Drift{Schedule: "0 */6 * * *", Notify: "desktop"})
	s.Apply()
	if st := s.Status(); st.Next == nil || st.Schedule != "0 */6 * * *" {
		t.Fatalf("%+v", st)
	}
	s.RunAll(context.Background())
	if len(*sent) != 1 || (*sent)[0].Count != 1 || !strings.Contains((*sent)[0].Text, "local_file.motd") {
		t.Fatalf("%+v", *sent)
	}
	s.RunAll(context.Background()) // same drift: no second notice
	if len(*sent) != 1 {
		t.Fatalf("notified again for unchanged drift: %d", len(*sent))
	}
	t.Setenv("FAKE_DRIFT_EXIT", "0") // drift fixed
	s.RunAll(context.Background())
	t.Setenv("FAKE_DRIFT_EXIT", "2") // and back: that's new again
	s.RunAll(context.Background())
	if len(*sent) != 2 {
		t.Fatalf("want a notice when drift reappears, got %d", len(*sent))
	}
}

func TestBadScheduleIsReported(t *testing.T) {
	s, _ := rig(t, config.Drift{Schedule: "every tuesday"})
	s.Apply()
	if st := s.Status(); st.Error == "" || st.Next != nil {
		t.Fatalf("%+v", st)
	}
}

func TestWebhookPayload(t *testing.T) {
	var got Notice
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(400)
		}
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	n := Notice{Text: "Drift in prod", Root: "envs/prod", Env: "prod", Count: 2, Addresses: []string{"a.b", "c.d"}}
	if err := Send(context.Background(), config.Drift{Notify: "webhook", Webhook: srv.URL}, n); err != nil {
		t.Fatal(err)
	}
	if got.Text != "Drift in prod" || got.Count != 2 {
		t.Fatalf("%+v", got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	if err := Send(context.Background(), config.Drift{Notify: "webhook", Webhook: bad.URL}, n); err == nil {
		t.Fatal("5xx must be an error")
	}
}
