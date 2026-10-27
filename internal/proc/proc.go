// Package proc runs subprocesses in their own process group, so stopping one
// (a cancelled context, an interrupt, a kill) reaches its children too:
// terraform's providers, ansible's forks, a linter's helpers.
package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Group puts cmd in a new process group, so Interrupt and Kill reach its
// children. Call it before cmd.Start.
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// GroupContext is Group for a command made with exec.CommandContext: when the
// context ends, the whole group is killed instead of only the direct child
// (CommandContext's default).
func GroupContext(cmd *exec.Cmd) {
	Group(cmd)
	cmd.Cancel = func() error { return Kill(cmd) }
}

// Interrupt sends SIGINT to cmd's process group (terraform releases its state
// lock on SIGINT; a second one makes it exit immediately).
func Interrupt(cmd *exec.Cmd) error { return signal(cmd, syscall.SIGINT) }

// Kill sends SIGKILL to cmd's process group.
func Kill(cmd *exec.Cmd) error { return signal(cmd, syscall.SIGKILL) }

func signal(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

// Alive reports whether any process of the group pgid still exists.
func Alive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	return syscall.Kill(-pgid, 0) == nil
}

// SecretPipes returns one pipe per value, each already holding its value and
// closed for writing, so a child can read it from /dev/fd/N (pass the files
// in cmd.ExtraFiles) and the value never touches the disk or the environment.
// The caller closes the files once the child has started. A value must fit in
// the pipe's buffer (a few KiB is always safe), or the write would block.
func SecretPipes(values []string) ([]*os.File, error) {
	var out []*os.File
	fail := func(err error) ([]*os.File, error) {
		ClosePipes(out)
		return nil, err
	}
	for _, v := range values {
		if len(v) > 4096 {
			return fail(errors.New("secret is too long for a pipe"))
		}
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		_, err = w.WriteString(v + "\n")
		w.Close()
		if err != nil {
			r.Close()
			return fail(err)
		}
		out = append(out, r)
	}
	return out, nil
}

// ClosePipes closes what SecretPipes returned.
func ClosePipes(fs []*os.File) {
	for _, f := range fs {
		f.Close()
	}
}
