package proc

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A cancelled context must end the whole group: here a shell and the sleep
// it started. Without Group, only the shell would die and sleep would linger.
func TestCancelKillsTheWholeGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo $!; wait")
	GroupContext(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	childPid, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if !Alive(cmd.Process.Pid) {
		t.Fatal("group should be alive")
	}
	cancel()
	_ = cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && exec.Command("kill", "-0", strconv.Itoa(childPid)).Run() == nil {
		time.Sleep(20 * time.Millisecond)
	}
	if exec.Command("kill", "-0", strconv.Itoa(childPid)).Run() == nil {
		exec.Command("kill", "-9", strconv.Itoa(childPid)).Run()
		t.Fatal("the grandchild survived the cancel")
	}
	if Alive(cmd.Process.Pid) {
		t.Fatal("group still alive")
	}
	if Alive(0) || Alive(-1) {
		t.Fatal("non-positive pgids are never alive")
	}
}
