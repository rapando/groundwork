package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/rapando/groundwork/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		var ee *cli.ExitError
		if errors.As(err, &ee) {
			if ee.Msg != "" {
				fmt.Fprintln(os.Stderr, "groundwork:", ee.Msg)
			}
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, "groundwork:", err)
		os.Exit(1)
	}
}
