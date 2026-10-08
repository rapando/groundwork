package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client talks to a running service on behalf of the CLI.
type Client struct {
	si   ServerInfo
	http *http.Client
}

func NewClient(si ServerInfo) *Client {
	return &Client{si: si, http: &http.Client{Timeout: 11 * time.Minute}} // a clone may be slow
}

func (c *Client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.si.Base()+"/api"+path, rd)
	if err != nil {
		return err
	}
	// the same credentials the browser holds; Origin satisfies the CSRF check
	req.Header.Set("X-Groundwork-Token", c.si.Token)
	req.Header.Set("Origin", c.si.Base())
	req.AddCookie(&http.Cookie{Name: "groundwork_session", Value: c.si.Token})
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return fmt.Errorf("%s", e.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) AddPath(path string) (Project, error) {
	var p Project
	return p, c.do("POST", "/projects", addRequest{Path: path}, &p)
}

func (c *Client) AddURL(u string) (Project, error) {
	var p Project
	return p, c.do("POST", "/projects", addRequest{URL: u}, &p)
}

func (c *Client) List() ([]ProjectView, error) {
	var out []ProjectView
	return out, c.do("GET", "/projects", nil, &out)
}

func (c *Client) Remove(id string) error {
	return c.do("DELETE", "/projects/"+url.PathEscape(id), nil, nil)
}
