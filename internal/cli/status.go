package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/store"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "打印视频状态表",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := cmd.Flags().GetString("state")
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.status(cmd.OutOrStdout(), state)
		},
	}
	cmd.Flags().String("state", "", "只显示该状态的视频（如 pending、failed）")
	return cmd
}

func (r *runtime) status(out io.Writer, state string) error {
	st, err := r.store()
	if err != nil {
		return err
	}
	videos, err := st.ListVideos(state)
	if err != nil {
		return err
	}
	if len(videos) == 0 {
		fmt.Fprintln(out, "没有匹配的视频记录。")
		return nil
	}
	stateFor := func(i int, v store.Video) string { return v.State }
	langFor := func(i int, v store.Video) string { return v.Lang }
	if err := writeVideosTable(out, videos, stateFor, langFor); err != nil {
		return err
	}
	fmt.Fprintf(out, "共 %d 个视频\n", len(videos))
	return nil
}
