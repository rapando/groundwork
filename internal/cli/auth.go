package cli

import (
	"bufio"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rapando/groundwork/internal/auth"
	"github.com/rapando/groundwork/internal/service"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Browser sign-in: see what is set up, or reset it if you are locked out",
		Long: "The console asks for a password or a passkey (Touch ID, Windows Hello, a security key).\n" +
			"The first visit from this machine sets them up; change them on the project list page.",
	}
	status := &cobra.Command{
		Use: "status", Short: "Show which sign-in methods are set up", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			a, err := auth.New(home, "")
			if err != nil {
				return err
			}
			st, err := a.Status()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !st.Configured {
				fmt.Fprintln(out, "not set up: the next browser visit from this machine sets sign-in up")
				return nil
			}
			fmt.Fprintln(out, "password:", map[bool]string{true: "set", false: "not set"}[st.Password])
			fmt.Fprintf(out, "passkeys: %d\n", len(st.Passkeys))
			for _, p := range st.Passkeys {
				used := "never used"
				if !p.LastUsed.IsZero() {
					used = "last used " + p.LastUsed.Local().Format(time.DateTime)
				}
				fmt.Fprintf(out, "  %s (added %s, %s)\n", p.Name, p.Created.Local().Format(time.DateOnly), used)
			}
			return nil
		},
	}
	var yes bool
	reset := &cobra.Command{
		Use:   "reset",
		Short: "Remove the password and every passkey and sign every browser out",
		Long: "Remove the password and every passkey and sign every browser out. The next visit from\n" +
			"this machine sets sign-in up again. A running service picks the change up at once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := service.Home()
			if err != nil {
				return err
			}
			if !yes && !confirm(cmd.OutOrStdout(), bufio.NewReader(cmd.InOrStdin()), "Remove every sign-in method and sign all browsers out?") {
				fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}
			if err := auth.Reset(home); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "sign-in reset: open the console to set it up again")
			return nil
		},
	}
	reset.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.AddCommand(status, reset)
	return cmd
}
