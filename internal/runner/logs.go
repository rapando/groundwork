package runner

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/terraform"
)

var (
	logHeadCap  int64 = 45 << 20 // bytes kept from the start of a run's log
	logTailKeep       = 2000     // lines kept from the end once the head is full
)

const batchEvery = 50 * time.Millisecond

// LogLine is one line of a run's log. Res carries structured per-resource
// progress for apply lines.
type LogLine struct {
	N     int                 `json:"n"`
	T     time.Time           `json:"t"`
	Stage string              `json:"stage"`
	Level string              `json:"level"` // debug | info | warn | error
	Text  string              `json:"text"`
	Res   *terraform.ResEvent `json:"res,omitempty"`
}

// logWriter appends redacted lines to log.ndjson and publishes them in
// batches. Runs that overflow the size cap keep their head and tail.
type logWriter struct {
	mu      sync.Mutex
	f       *os.File
	n       int
	bytes   int64
	omitted int
	tail    []LogLine
	pending []LogLine
	timer   *time.Timer
	publish func([]LogLine)
	closed  bool
}

func openLog(path string, publish func([]LogLine)) (*logWriter, error) {
	n := 0
	if b, err := os.ReadFile(path); err == nil { // continuing a run (apply after approval)
		for _, l := range strings.Split(string(b), "\n") {
			if l == "" {
				continue
			}
			var ll LogLine
			if json.Unmarshal([]byte(l), &ll) == nil && ll.N >= n {
				n = ll.N + 1
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &logWriter{f: f, n: n, publish: publish}, nil
}

func (w *logWriter) add(stage, level, text string, res *terraform.ResEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	l := LogLine{N: w.n, T: time.Now().UTC(), Stage: stage, Level: level, Text: text, Res: res}
	w.n++
	b, _ := json.Marshal(l)
	if w.bytes+int64(len(b)) <= logHeadCap {
		_, _ = w.f.Write(append(b, '\n')) // best effort: live viewers still get the line
		w.bytes += int64(len(b)) + 1
	} else {
		w.omitted++
		w.tail = append(w.tail, l)
		if len(w.tail) > logTailKeep {
			w.tail = w.tail[1:]
		}
	}
	w.pending = append(w.pending, l) // live viewers see everything
	if w.timer == nil {
		w.timer = time.AfterFunc(batchEvery, w.flush)
	}
}

func (w *logWriter) flush() {
	w.mu.Lock()
	batch := w.pending
	w.pending, w.timer = nil, nil
	w.mu.Unlock()
	if len(batch) > 0 && w.publish != nil {
		w.publish(batch)
	}
}

func (w *logWriter) close() {
	w.flush()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	if w.omitted > 0 {
		dropped := w.omitted - len(w.tail)
		if dropped > 0 {
			m, _ := json.Marshal(LogLine{N: w.n, T: time.Now().UTC(), Level: "warn",
				Text: "… log too large: " + itoa(dropped) + " lines omitted …"})
			_, _ = w.f.Write(append(m, '\n'))
		}
		for _, l := range w.tail {
			b, _ := json.Marshal(l)
			_, _ = w.f.Write(append(b, '\n'))
		}
	}
	w.f.Close()
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// LogPage is one page of a run's log.
type LogPage struct {
	Lines []LogLine `json:"lines"`
	Next  int       `json:"next"`  // pass as `from` to continue
	Total int       `json:"total"` // lines in the file (before filtering)
}

// ReadLog returns up to limit lines with N >= from, filtered by minimum level
// and a case-insensitive substring.
func ReadLog(path string, from, limit int, minLevel, q string) (*LogPage, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	page := &LogPage{Lines: []LogLine{}, Next: from}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return page, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	min := levelRank[minLevel]
	q = strings.ToLower(q)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		page.Total++
		if l.N < from || len(page.Lines) >= limit {
			continue
		}
		if levelRank[l.Level] < min || (q != "" && !strings.Contains(strings.ToLower(l.Text), q)) {
			page.Next = l.N + 1
			continue
		}
		page.Lines = append(page.Lines, l)
		page.Next = l.N + 1
	}
	return page, sc.Err()
}

// ReadResources folds a run's log into the latest state per resource.
func ReadResources(path string) ([]terraform.ResEvent, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []terraform.ResEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	idx := map[string]int{}
	out := []terraform.ResEvent{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Res == nil {
			continue
		}
		if i, ok := idx[l.Res.Address]; ok {
			out[i] = *l.Res
		} else {
			idx[l.Res.Address] = len(out)
			out = append(out, *l.Res)
		}
	}
	return out, sc.Err()
}

// LogText returns a run's log as plain text (already redacted), keeping the
// last maxBytes: errors are at the end.
func (r *Runner) LogText(id int64, maxBytes int) string {
	f, err := os.Open(r.logPath(id))
	if err != nil {
		return ""
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
	}
	s := b.String()
	if len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
	}
	return s
}

// Running reports whether a job is active on the given lock scope:
// a Terraform root, or an Ansible project#env.
func (r *Runner) Running(tfRoot, ansScope string) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	for id, j := range r.jobs {
		if (tfRoot != "" && !j.target.isAnsible() && j.target.Root == tfRoot) || (ansScope != "" && j.target.isAnsible() && j.target.Scope() == ansScope) {
			ids = append(ids, id)
		}
	}
	return ids
}
