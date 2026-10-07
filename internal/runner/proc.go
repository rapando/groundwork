package runner

import (
	"os/exec"
	"syscall"
)

// Each job runs in its own process group so a cancel reaches terraform's
// children (providers, provisioners) and not just the CLI.
func setProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func interrupt(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
func kill(cmd *exec.Cmd)      { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
