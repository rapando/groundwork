package main

import (
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	handler("dev")(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || rec.Body.String() != "hello from dev\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}
