package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// newServeCmd is a placeholder: Task 16 replaces its RunE with the HTTP API and
// embedded UI, and its --addr flag is registered now so the command line is
// already stable.
func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动 Web 界面（Task 16）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("serve: not implemented until Task 16")
		},
	}
	cmd.Flags().String("addr", ":8080", "监听地址")
	return cmd
}
