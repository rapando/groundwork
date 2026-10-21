package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// webhookClient has its own timeout and follows no redirects: the URL was
// validated as http(s) with a host, and a redirect could point elsewhere.
var webhookClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// PostWebhook posts payload as JSON to url. A non-2xx answer is an error.
func PostWebhook(ctx context.Context, url string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "groundwork")
	res, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", res.Status)
	}
	return nil
}

// Desktop shows a desktop notification with osascript (macOS) or
// notify-send (elsewhere).
func Desktop(ctx context.Context, title, text string) error {
	return desktopCmd(ctx, runtime.GOOS, title, text).Run()
}

// desktopCmd builds the command. Title and text go in as argv, never into
// a script, so nothing in them is interpreted, and "--" ends the options so
// a title starting with "-" can't become one (osascript would otherwise take
// "-e" as another script line).
func desktopCmd(ctx context.Context, goos, title, text string) *exec.Cmd {
	if goos == "darwin" {
		return exec.CommandContext(ctx, "osascript",
			"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", title, text)
	}
	return exec.CommandContext(ctx, "notify-send", "--app-name=groundwork", "--", title, text)
}
