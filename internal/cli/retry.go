package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/store"
)

func newRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry",
		Short: "把 failed 视频重置为 pending",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.retry(cmd.OutOrStdout())
		},
	}
}

func (r *runtime) retry(out io.Writer) error {
	st, err := r.store()
	if err != nil {
		return err
	}
	videos, err := st.ListVideos(store.StateFailed)
	if err != nil {
		return err
	}
	for _, v := range videos {
		// Keep the video's lang: retry only re-queues it, probe/download decides
		// the next state.
		if err := st.SetState(v.ID, store.StatePending, v.Lang); err != nil {
			return fmt.Errorf("retry %s: %w", v.Bvid, err)
		}
	}
	fmt.Fprintf(out, "已把 %d 个失败视频重置为 pending，请重新运行 probe <url> 再重试。\n", len(videos))
	return nil
}
