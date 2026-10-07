package checks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// Cmd is one external command invocation.
type Cmd struct {
	Dir     string
	Env     []string // extra KEY=VALUE, appended to the inherited environment
	Argv    []string
	Timeout time.Duration
}

type Output struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Exec abstracts process execution so checks can be tested with recorded output.
type Exec interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, c Cmd) (Output, error)
}

type OSExec struct{}

func (OSExec) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Run executes argv (never through a shell). A non-zero exit is not an error:
// linters use exit codes to signal findings, so it is returned in Output.
func (OSExec) Run(ctx context.Context, c Cmd) (Output, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	out := Output{Stdout: so.Bytes(), Stderr: se.Bytes()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		out.ExitCode = ee.ExitCode()
		err = nil
	case ctx.Err() != nil:
		err = ctx.Err()
	}
	return out, err
}
