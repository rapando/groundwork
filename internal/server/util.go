package server

import (
	"bytes"
	"net/url"
	"time"
)

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

func replaceOnce(b, old, new []byte) []byte { return bytes.Replace(b, old, new, 1) }

func newTicker() *time.Ticker { return time.NewTicker(15 * time.Second) }
