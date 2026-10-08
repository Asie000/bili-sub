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
	if err := checkResetPath(dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "已清空数据目录 %s\n", dir)
	return nil
}

// checkResetPath refuses shapes that would wipe the working directory or a
// filesystem root (DataDir comes from a hand-edited .env): an empty path, the
// working directory, its parent, a bare separator, or any filesystem root. A
// root cleans to itself, so filepath.Dir(p) == p catches `/`, `\` and Windows
// volume roots such as `C:\`.
func checkResetPath(dataDir string) error {
	p := filepath.Clean(dataDir)
	// Both separators are listed literally: filepath.Clean normalizes `/` to
	// `\` on Windows, so the path never needs filepath.Separator here (which
	// would also collide with the `\\` case at compile time).
	switch p {
	case "", ".", "..", "/", `\`:
		return fmt.Errorf("reset: 拒绝删除数据目录 %q", dataDir)
	}
	if filepath.Dir(p) == p {
		return fmt.Errorf("reset: 拒绝删除数据目录 %q", dataDir)
	}
	return nil
}
