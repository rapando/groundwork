package runner

import (
	"os/exec"

	"github.com/rapando/groundwork/v2/internal/proc"
)

// Each job runs in its own process group so a cancel reaches terraform's
// children (providers, provisioners) and not just the CLI.
func setProcAttr(cmd *exec.Cmd) { proc.Group(cmd) }

func interrupt(cmd *exec.Cmd) { _ = proc.Interrupt(cmd) }
func kill(cmd *exec.Cmd)      { _ = proc.Kill(cmd) }
