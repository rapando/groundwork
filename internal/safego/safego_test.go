package safego

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunRecoversLogsAndReports(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var got atomic.Value
	ok := Run(log, "thing", func(name string, v any) { got.Store(name + ": " + v.(string)) }, func() { panic("boom") })
	if ok {
		t.Fatal("a panic is not ok")
	}
	if g, _ := got.Load().(string); g != "thing: boom" {
		t.Fatalf("hook got %q", g)
	}
	if s := buf.String(); !strings.Contains(s, "boom") || !strings.Contains(s, "safego_test.go") || !strings.Contains(s, "where=thing") {
		t.Fatalf("log lacks the panic or stack: %s", s)
	}
	if !Run(log, "fine", nil, func() {}) {
		t.Fatal("a normal return is ok")
	}
	// a hook that itself panics is contained too
	if Run(log, "x", func(string, any) { panic("hook") }, func() { panic("inner") }) {
		t.Fatal("should report the panic")
	}
}

func TestGoDoesNotTakeTheCallerDown(t *testing.T) {
	done := make(chan struct{})
	Go(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), "bg", func(string, any) { close(done) }, func() { panic("bg boom") })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hook never called")
	}
}

func TestLoopRestartsAfterAPanicUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var starts atomic.Int32
	Loop(ctx, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), "loop", nil, func(ctx context.Context) {
		if starts.Add(1) == 1 {
			panic("first run dies")
		}
		<-ctx.Done()
	})
	deadline := time.Now().Add(10 * time.Second)
	for starts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if starts.Load() != 2 {
		t.Fatalf("loop restarted %d times, want 2 starts", starts.Load())
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	if starts.Load() != 2 {
		t.Fatal("a loop that returned because ctx ended must not restart")
	}
}
