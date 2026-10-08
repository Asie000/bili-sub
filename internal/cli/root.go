// Package cli wires the internal packages into the bili-sub command tree.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/config"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/store"
	"bilibili-sub/internal/util"
)

// NewRootCmd builds the bili-sub command tree. Every command resolves its
// config through a per-invocation runtime built from the persistent --env flag.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "bili-sub",
		Short: "bilibili 字幕采集与本地 ASR 流水线",
		// A failed command should print its error, not a wall of usage text.
		SilenceUsage: true,
	}
	root.PersistentFlags().String("env", ".env", "配置文件路径（KEY=VALUE）")

	root.AddCommand(
		newLoginCmd(),
		newProbeCmd(),
		newDownloadCmd(),
		newTranscribeCmd(),
		newExportCmd(),
		newStatusCmd(),
		newRetryCmd(),
		newServeCmd(),
		newResetCmd(),
	)
	return root
}

// runtime carries the per-invocation config. The store is opened on first use
// so commands that never touch the database (login, reset) do not create one.
type runtime struct {
	cfg *config.Config
	st  *store.Store
}

func newRuntime(cmd *cobra.Command) (*runtime, error) {
	env, err := cmd.Flags().GetString("env")
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	return &runtime{cfg: cfg}, nil
}

// store opens (and caches) the SQLite database, creating DataDir if needed.
func (r *runtime) store() (*store.Store, error) {
	if r.st != nil {
		return r.st, nil
	}
	if err := os.MkdirAll(r.cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	st, err := store.Open(r.cfg.DBPath)
	if err != nil {
		return nil, err
	}
	r.st = st
	return st, nil
}

// close releases the store when a command opened one. Command RunE funcs defer
// it so the SQLite handle is gone before the process (or a test) removes
// DataDir.
func (r *runtime) close() {
	if r.st != nil {
		r.st.Close()
	}
}

// sessionPath is the persisted SESSDATA file shared by login and the API
// commands.
func (r *runtime) sessionPath() string {
	return filepath.Join(r.cfg.DataDir, "session.json")
}

// subtitleDir holds the .srt files written by the download command.
func (r *runtime) subtitleDir() string {
	return filepath.Join(r.cfg.DataDir, "subtitles")
}

// client builds an API client from the persisted session.json, falling back to
// the SESSDATA from the config file.
func (r *runtime) client() (*bili.Client, error) {
	sessdata := r.cfg.SESSDATA
	saved, err := bili.LoadSESSDATA(r.sessionPath())
	if err != nil {
		return nil, err
	}
	if saved != "" {
		sessdata = saved
	}
	return bili.NewClient(sessdata, bili.NewRateLimiter(r.cfg.QPS)), nil
}

// supportedFormats is the set export.Render can produce; --format rejects
// anything else so a typo fails before any file is written.
var supportedFormats = map[string]bool{
	export.FormatSRT:      true,
	export.FormatText:     true,
	export.FormatMarkdown: true,
	export.FormatJSON:     true,
}

// parseFormats splits a comma-separated --format value, rejecting unknown
// formats and dropping duplicates while keeping the caller's order.
func parseFormats(s string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !supportedFormats[part] {
			return nil, fmt.Errorf("unsupported format %q (want one of srt,txt,md,json)", part)
		}
		if seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, errors.New("no format given (want one of srt,txt,md,json)")
	}
	return out, nil
}

// writeVideosTable prints the shared 序号/标题/作者/发布日期/状态/语言/充电 table.
// stateFor and langFor render the two per-video columns the callers derive
// differently (probe results vs. stored states).
func writeVideosTable(w io.Writer, videos []store.Video, stateFor, langFor func(i int, v store.Video) string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "序号\t标题\t作者\t发布日期\t状态\t语言\t充电")
	for i, v := range videos {
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i+1, v.Title, v.Author, formatPubdate(v.Pubdate), stateFor(i, v), langFor(i, v), formatCharging(v.IsCharging)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// pageStem is the filename stem shared by every rendered format of one video.
// It must match the .srt name chosen by pipeline.Downloader.FetchSubtitle: the
// _P{n} suffix only appears for multi-page videos, so pages of one video never
// collide.
func pageStem(v store.Video) string {
	base := util.SanitizeFilename(v.Title, v.Bvid)
	if v.PageIndex > 1 {
		base += "_P" + strconv.Itoa(v.PageIndex)
	}
	return base
}

// formatPubdate renders a unix pubdate as YYYY-MM-DD, or "-" when unknown.
func formatPubdate(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return time.Unix(sec, 0).Format("2006-01-02")
}

// formatCharging renders the 充电 column.
func formatCharging(isCharging bool) string {
	if isCharging {
		return "是"
	}
	return "否"
}
