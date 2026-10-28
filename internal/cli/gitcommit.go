package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/rapando/groundwork/v2/internal/scaffold"
)

// commitScaffold commits only the files init just wrote, leaving other staged work alone.
func commitScaffold(out io.Writer, root string, res []scaffold.Result) error {
	var paths []string
	for _, r := range res {
		if r.Status != "skipped" {
			paths = append(paths, r.Path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err := os.Stat(root + "/.git"); err != nil {
		return fmt.Errorf("--git-commit: %s is not a git repository", root)
	}
	run := func(args ...string) error {
		b, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %v\n%s", args[0], err, b)
		}
		return nil
	}
	if err := run(append([]string{"add", "--"}, paths...)...); err != nil {
		return err
	}
	if err := run(append([]string{"commit", "-m", "chore: set up groundwork", "--"}, paths...)...); err != nil {
		return err
	}
	fmt.Fprintln(out, "committed: chore: set up groundwork")
	return nil
}
