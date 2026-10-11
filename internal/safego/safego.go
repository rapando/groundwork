// Package safego runs background work so that a panic in one goroutine is
// logged and reported instead of taking the whole service (every project)
// down. HTTP handlers are covered by chi's Recoverer; this is for everything
// else: job runners, watchers, schedulers, timers.
package safego

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// Hook is told about a recovered panic: where it happened and the value.
type Hook func(name string, v any)

// Go runs fn on a new goroutine with Run's protection.
func Go(log *slog.Logger, name string, onPanic Hook, fn func()) {
	go Run(log, name, onPanic, fn)
}

// Run calls fn and recovers a panic: it is logged at Error with the stack
// and passed to onPanic (if any). Run returns true if fn completed normally.
func Run(log *slog.Logger, name string, onPanic Hook, fn func()) (ok bool) {
	defer func() {
		if v := recover(); v != nil {
			if log == nil {
				log = slog.Default()
			}
			log.Error("internal error (recovered)", "where", name, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if onPanic != nil {
				// a panicking hook must not take the service down either
				func() {
					defer func() { _ = recover() }()
					onPanic(name, v)
				}()
			}
		}
	}()
	fn()
	return true
}

// Loop runs fn on a new goroutine and, if it panics, starts it again after a
// backoff (1s, doubling to a minute) until ctx is done. fn is expected to
// return on its own only when ctx is done; a loop that returns normally is
// not restarted. The returned channel is closed once the loop has stopped
// for good, so owners can wait for it before closing what it uses.
func Loop(ctx context.Context, log *slog.Logger, name string, onPanic Hook, fn func(ctx context.Context)) <-chan struct{} {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		delay := time.Second
		for {
			if Run(log, name, onPanic, func() { fn(ctx) }) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < time.Minute {
				delay *= 2
			}
		}
	}()
	return stopped
}
