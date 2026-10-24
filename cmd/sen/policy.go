package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
)

func newPolicyCmd() *cobra.Command {
	var homes []string
	var check bool
	var policyFile string
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Print the Tetragon policy sentient needs (does not install it)",
		Long: `Print the Tetragon TracingPolicy that makes Tetragon report your coding agent's
process tree to sentient. It only prints; nothing is installed or changed. Install it yourself:

  sen policy | sudo tee /etc/tetragon/tetragon.tp.d/sentient.yaml
  sudo systemctl restart tetragon

The policy names the agent binaries installed now, so run it again, and restart
Tetragon, after the agent updates.

sen policy --check compares the installed policy with the agents installed now
and warns about agents that started before Tetragon loaded it (their commands
are recorded, but not the files and connections those commands touch). It needs
no privileges and exits 1 when anything is wrong.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(homes) == 0 {
				h, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("no home directory (use --home): %w", err)
				}
				homes = []string{h}
			}
			if check {
				rep := policy.Check(policy.CheckOptions{PolicyFile: policyFile, Homes: homes})
				w := cmd.OutOrStdout()
				for _, l := range rep.Lines {
					mark := " "
					if !l.OK {
						mark = "!"
					}
					fmt.Fprintf(w, "%s %-9s %s\n", mark, l.Label+":", l.Text)
				}
				if !rep.OK() {
					cmd.SilenceUsage = true
					return fmt.Errorf("policy check failed")
				}
				return nil
			}
			bins := policy.Discover(homes)
			if len(bins) == 0 {
				return fmt.Errorf("no agent found under %v (use --home for another user's home)", homes)
			}
			p, err := policy.Render(bins)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), p)
			return err
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check the installed policy against the agents installed and running now; exit 1 if anything is wrong")
	cmd.Flags().StringVar(&policyFile, "policy-file", policy.DefaultPolicyFile, "the installed policy --check reads")
	cmd.Flags().StringSliceVar(&homes, "home", nil, "home directory whose agent installs the policy names; repeatable (default your home)")
	return cmd
}
