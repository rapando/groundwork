package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rapando/groundwork/internal/terraform"
)

var errCancelled = errors.New("cancelled")

type execOpts struct {
	stage    string
	argv     []string
	jsonMode bool         // terraform -json stream: parse events, log their text
	capture  io.Writer    // stdout goes here instead of the log (e.g. `show -json`, which may hold secrets)
	env      []string     // extra KEY=VALUE
	dir      string       // working directory (default: repo root)
	events   func([]byte) // receives lines written to file descriptor 3 (ansible callback)
}

type execResult struct {
	exit   int
	killed bool // had to SIGKILL after the grace period
}

var tfEnv = []string{"TF_IN_AUTOMATION=1", "TF_INPUT=0", "NO_COLOR=1", "CHECKPOINT_DISABLE=1"}

// execute runs one command for a job, streaming its output into the job's log.
// On cancellation the whole process group gets SIGINT (so terraform can
// release its state lock), then SIGKILL after KillGrace.
func (r *Runner) execute(j *job, o execOpts) (execResult, error) {
	bin, err := exec.LookPath(o.argv[0])
	if err != nil {
		return execResult{}, fmt.Errorf("%s not found on PATH", o.argv[0])
	}
	cmd := exec.Command(bin, o.argv[1:]...) // argv slice: no shell, ever
	cmd.Dir = r.Root
	if o.dir != "" {
		cmd.Dir = o.dir
	}
	var evR, evW *os.File
	if o.events != nil {
		var perr error
		evR, evW, perr = os.Pipe()
		if perr != nil {
			return execResult{}, perr
		}
		cmd.ExtraFiles = []*os.File{evW} // fd 3 in the child
		defer evR.Close()
	}
	cmd.Env = append(append(os.Environ(), tfEnv...), o.env...)
	setProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return execResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return execResult{}, err
	}
	if j.ctx.Err() != nil {
		return execResult{}, errCancelled
	}
	if err := cmd.Start(); err != nil {
		if evW != nil {
			evW.Close()
		}
		return execResult{}, err
	}
	if evW != nil {
		evW.Close() // the child holds its own copy; EOF arrives when it exits
	}

	var killed atomic.Bool
	done := make(chan struct{})
	go func() {
		select {
		case <-j.ctx.Done():
			interrupt(cmd)
			t := time.NewTimer(r.KillGrace)
			defer t.Stop()
			select {
			case <-done:
			case <-t.C:
				killed.Store(true)
				kill(cmd)
			}
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	if evR != nil {
		wg.Add(1)
		go func() { defer wg.Done(); readLines(evR, func(l string) { o.events([]byte(l)) }) }()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		if o.capture != nil {
			_, _ = io.Copy(o.capture, stdout)
			return
		}
		readLines(stdout, func(l string) { r.handleLine(j, o.stage, l, o.jsonMode) })
	}()
	go func() {
		defer wg.Done()
		readLines(stderr, func(l string) { r.handleLine(j, o.stage, l, false) })
	}()
	wg.Wait()
	werr := cmd.Wait()
	close(done)

	res := execResult{killed: killed.Load()}
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.exit = ee.ExitCode()
	default:
		return res, werr
	}
	if j.ctx.Err() != nil {
		return res, errCancelled
	}
	return res, nil
}

// readLines calls fn for every line, truncating pathological ones.
func readLines(rd io.Reader, fn func(string)) {
	br := bufio.NewReaderSize(rd, 64<<10)
	const max = 1 << 20
	var buf []byte
	for {
		chunk, prefix, err := br.ReadLine()
		if len(buf) < max {
			buf = append(buf, chunk...)
		}
		if !prefix {
			if len(buf) > 0 || err == nil {
				fn(strings.TrimRight(string(buf), "\r"))
			}
			buf = buf[:0]
		}
		if err != nil {
			return
		}
	}
}

func (r *Runner) handleLine(j *job, stage, line string, jsonMode bool) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if jsonMode {
		if ev, ok := terraform.ParseLine([]byte(line)); ok {
			if ev.Type == "version" || ev.Type == "log" && ev.Message == "" {
				return
			}
			j.mu.Lock()
			if ev.Changes != nil && ev.Changes.Operation == "apply" {
				j.apply = &ApplySummary{Added: ev.Changes.Add, Changed: ev.Changes.Change, Destroyed: ev.Changes.Remove}
			}
			if len(ev.Outputs) > 0 && j.apply != nil {
				j.apply.Outputs = ev.Outputs
			}
			j.mu.Unlock()
			j.emit(stage, ev.Level, ev.Text(), ev.Res)
			return
		}
	}
	level := "info"
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "│"))
	switch {
	case strings.HasPrefix(t, "Error:"):
		level = "error"
	case strings.HasPrefix(t, "Warning:"):
		level = "warn"
	}
	j.emit(stage, level, line, nil)
}

// emit redacts and records one log line.
func (j *job) emit(stage, level, text string, res *terraform.ResEvent) {
	if j.red != nil {
		text = j.red.Line(text)
	}
	if level == "error" {
		first := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
		j.mu.Lock()
		if strings.HasPrefix(first, "Error:") {
			if j.lastErr == "" {
				j.lastErr = strings.TrimSpace(strings.TrimPrefix(first, "Error:"))
			}
		} else if j.fallbackErr == "" {
			j.fallbackErr = first
		}
		j.mu.Unlock()
	}
	if j.log != nil {
		j.log.add(stage, level, text, res)
	}
}
