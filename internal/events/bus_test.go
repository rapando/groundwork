package events

import "testing"

func TestPublishSubscribe(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	b.Publish("x", map[string]int{"a": 1})
	ev := <-ch
	if ev.Type != "x" || string(ev.Data) != `{"a":1}` {
		t.Fatalf("got %+v", ev)
	}
	cancel()
	cancel() // idempotent
	b.Publish("y", nil)
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBus()
	_, cancel := b.Subscribe()
	defer cancel()
	for i := 0; i < 1000; i++ {
		b.Publish("x", nil)
	}
}
