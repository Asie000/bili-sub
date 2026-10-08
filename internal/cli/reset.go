package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newResetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "清空本地数据目录（需要 --yes）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			yes, err := cmd.Flags().GetBool("yes")
			if err != nil {
				return err
			}
			if !yes {
				return errors.New("reset: 会删除整个数据目录，需要 --yes 确认")
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.reset(cmd.OutOrStdout())
		},
	}
	cmd.Flags().Bool("yes", false, "确认删除数据目录内容")
	return cmd
}

func (r *runtime) reset(out io.Writer) error {
	dir := r.cfg.DataDir
	// Refuse the shapes that would wipe the working directory or a filesystem
	// root; DataDir comes from a hand-edited .env.
	switch dir {
	case "", ".", string(filepath.Separator):
		return fmt.Errorf("reset: 拒绝删除数据目录 %q", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "已清空数据目录 %s\n", dir)
	return nil
}
