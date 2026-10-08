package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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

// checkResetPath refuses shapes that would wipe the working directory, a
// directory above it, or a filesystem root (DataDir comes from a hand-edited
// .env): an empty path, the working directory, its parent or any ancestor, a
// bare separator, a filesystem root, or a Windows drive-relative path.
func checkResetPath(dataDir string) error {
	refuse := func() error {
		return fmt.Errorf("reset: 拒绝删除数据目录 %q", dataDir)
	}
	p := filepath.Clean(dataDir)
	// Both separators are listed literally: filepath.Clean normalizes `/` to
	// `\` on Windows, so the path never needs filepath.Separator here (which
	// would also collide with the `\\` case at compile time).
	switch p {
	case "", ".", "..", "/", `\`:
		return refuse()
	}
	// A root cleans to itself, so filepath.Dir(p) == p catches `/`, `\` and
	// Windows volume roots such as `C:\`.
	if filepath.Dir(p) == p {
		return refuse()
	}
	// Multi-level escapes such as `../..` clean to a path whose Dir is still
	// `..`, so the single `..` case above is not enough: os.RemoveAll would
	// delete the grandparent of the working directory.
	if strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return refuse()
	}
	// A drive-relative Windows value such as `C:` has a volume but no root, so
	// filepath.Dir does not flag it and os.RemoveAll would resolve it against
	// the current directory of that drive.
	if v := filepath.VolumeName(p); v != "" && len(p) == len(v) {
		return refuse()
	}
	return nil
}
