package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
)

// progressInterval throttles per-item progress callbacks to about five calls
// per second, so a large source does not flood the SSE stream. A final tick
// (done == total) always goes through.
const progressInterval = 200 * time.Millisecond

// newEnumerator is a variable so tests can point the probe pipeline at fake
// servers; production always uses the real constructor. This mirrors the
// newQRLoginClient seam in serve.go.
var newEnumerator = pipeline.NewEnumerator

// newProgressEmitter returns a per-item progress callback that forwards a tick
// through emit, suppressing ticks that arrive within progressInterval of the
// previous one. The first tick and any final tick (done == total) are always
// emitted, so a caller can rely on the start and end of a run being reported. A
// nil emit makes every tick a no-op.
func newProgressEmitter(total int, emit func(done, total int)) func(done int) {
	if emit == nil {
		return func(int) {}
	}
	var mu sync.Mutex
	var last time.Time
	return func(done int) {
		mu.Lock()
		if done < total && time.Since(last) < progressInterval {
			mu.Unlock()
			return
		}
		last = time.Now()
		mu.Unlock()
		emit(done, total)
	}
}

func newProbeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "probe <url>",
		Short: "枚举来源并探测字幕可用性（不下载）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := cmd.Flags().GetString("kind")
			if err != nil {
				return err
			}
			force, err := cmd.Flags().GetBool("force")
			if err != nil {
				return err
			}
			limit, err := searchLimit(cmd)
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.probe(cmd.Context(), cmd.OutOrStdout(), args[0], kind, force, limit, nil)
			return err
		},
	}
	cmd.Flags().String("kind", "", "来源类型（single/space/fav/season/search），空则按 URL 自动识别")
	cmd.Flags().Bool("force", false, "重新列出并重置已完成的行（fetched/transcribed/exported）为待处理，让它们被重新检查/下载")
	cmd.Flags().Int("limit", pipeline.DefaultSearchLimit, "搜索来源最多取多少条（0 = 不限）；其他来源忽略此项")
	return cmd
}

// searchLimit validates the --limit flag. Negative used to mean "unlimited" in
// silence, because the check is `limit > 0` -- one dropped minus sign away from
// walking a thousand results and a thousand view calls.
func searchLimit(cmd *cobra.Command) (int, error) {
	limit, err := cmd.Flags().GetInt("limit")
	if err != nil {
		return 0, err
	}
	if limit < 0 {
		return 0, errors.New("--limit 不能为负数（0 表示不限）")
	}
	return limit, nil
}

// probeStats is what one probe run learned about its source. Both the CLI
// summary line and the HTTP /api/probe ProbeSummary are built from it.
type probeStats struct {
	Total          int
	Downloadable   int
	NoSubtitle     int
	ChargingLocked int
	Failed         int
	Skipped        int
	// Hint is a sentence for the user that is not a count, currently the 合集 the
	// probed video turned out to belong to. It is a value rather than a direct
	// print so the CLI and the web summary show the same thing.
	Hint string
	// FirstError is the first per-video failure reason. serve passes io.Discard
	// for the CLI writer, so a reason that only went to `out` would reach the
	// command line but never the browser; carrying it on the summary lets the
	// web page show why a run failed instead of a bare count.
	FirstError string
}

// resolveInputURL expands a b23.tv share link into its real bilibili URL before
// anything is parsed. Every other input is returned unchanged, so callers may run
// it unconditionally. It needs the client, which is why it lives here and not in
// the pure parser.
func (r *runtime) resolveInputURL(ctx context.Context, raw string) (string, error) {
	if !pipeline.IsShortLink(raw) {
		return raw, nil
	}
	client, err := r.client()
	if err != nil {
		return "", err
	}
	return pipeline.ResolveShortLink(ctx, client, raw)
}

func (r *runtime) probe(ctx context.Context, out io.Writer, rawURL, kindSpec string, force bool, limit int, onProgress func(done, total int)) (probeStats, error) {
	var stats probeStats
	var kind pipeline.SourceKind
	var key string
	rawURL, err := r.resolveInputURL(ctx, rawURL)
	if err != nil {
		return stats, err
	}
	if kindSpec != "" {
		explicit, ok := pipeline.ParseSourceKind(kindSpec)
		if !ok {
			return stats, fmt.Errorf("probe: 未知来源类型 %q", kindSpec)
		}
		kind = explicit
		key, err = pipeline.ParseURLForKind(rawURL, kind)
	} else {
		kind, key, err = pipeline.ParseURL(rawURL)
	}
	if err != nil {
		return stats, err
	}
	client, err := r.client()
	if err != nil {
		return stats, err
	}
	en := newEnumerator(client)
	en.Workers = r.cfg.Concurrency

	infos, err := collectVideos(ctx, en, kind, key, limit)
	if err != nil {
		if sessionExpired(err) {
			msg := "登录态失效（" + err.Error() + "）。请先运行 `bili-sub login`。"
			fmt.Fprintln(out, msg)
			stats.FirstError = msg
		}
		if riskControl(err) {
			msg := "请求被 B站风控限流，已停止列出（" + err.Error() + "）；等几分钟再重试。"
			fmt.Fprintln(out, msg)
			stats.FirstError = msg
		}
		return stats, err
	}
	if kind == pipeline.KindSingle {
		// One video can be one episode of a 合集. Rather than quietly turning a
		// 1-video request into 31 videos and 30x the requests, say so and hand over
		// the link that would collect the rest.
		if stats.Hint = seasonHint(infos); stats.Hint != "" {
			fmt.Fprintln(out, stats.Hint)
		}
	}
	st, err := r.store()
	if err != nil {
		return stats, err
	}

	var videos []store.Video
	for _, vi := range infos {
		videos = append(videos, expandPages(vi, key)...)
	}
	for i := range videos {
		id, err := st.UpsertVideo(&videos[i])
		if err != nil {
			return stats, fmt.Errorf("probe %s: 写入 videos: %w", videos[i].Bvid, err)
		}
		videos[i].ID = id
	}
	if len(videos) == 0 {
		fmt.Fprintln(out, "该来源没有视频。")
		return stats, nil
	}
	stats.Total = len(videos)
	// Listing stops at enumeration and cid resolution: it deliberately does NOT
	// ask the player endpoint whether each video has subtitles. That check moved
	// into the download step (which records no_subtitle / charging_locked),
	// because doing it here sent two requests per video in the step users run
	// first -- the one that is meant to feel quick. New rows are left pending;
	// rows that already existed keep their state (UpsertVideo never touches state
	// on conflict), so re-listing a source never clobbers a downloaded one.
	if force {
		// --force now means "forget that these were already handled": reset the
		// completed rows so the next download re-checks and re-fetches them.
		existing, err := st.ListVideosFiltered("", key)
		if err != nil {
			return stats, err
		}
		for _, v := range existing {
			switch v.State {
			case store.StateFetched, store.StateTranscribed, store.StateExported:
				if err := st.SetState(v.ID, store.StatePending, ""); err != nil {
					return stats, err
				}
			}
		}
	}
	if onProgress != nil {
		onProgress(0, stats.Total)
		onProgress(stats.Total, stats.Total)
	}
	fmt.Fprintf(out, "已列出 %d 条（来源 %s）。\n", stats.Total, key)
	fmt.Fprintln(out, "下一步：`bili-sub download` 会检查每个视频有没有字幕并下载；没字幕的标为「无字幕」，之后可用 `bili-sub transcribe` 转写。")
	return stats, nil
}

// collectVideos turns a parsed source into full VideoInfo records.
func collectVideos(ctx context.Context, en *pipeline.Enumerator, kind pipeline.SourceKind, key string, limit int) ([]*bili.VideoInfo, error) {
	switch kind {
	case pipeline.KindSingle:
		vi, err := en.ResolveVideo(ctx, key)
		if err != nil {
			return nil, err
		}
		return []*bili.VideoInfo{vi}, nil
	case pipeline.KindSpace:
		return en.EnumerateSpace(ctx, key)
	case pipeline.KindFav:
		return en.EnumerateFav(ctx, key)
	case pipeline.KindSeason:
		mid, seasonID, ok := pipeline.SplitSeasonKey(key)
		if !ok {
			return nil, fmt.Errorf("probe: 合集来源键格式不对：%q", key)
		}
		return en.EnumerateSeason(ctx, mid, seasonID)
	case pipeline.KindSearch:
		keyword, ok := pipeline.SplitSearchKey(key)
		if !ok {
			return nil, fmt.Errorf("probe: 搜索来源键格式不对：%q", key)
		}
		return en.EnumerateSearch(ctx, keyword, limit)
	default:
		return nil, fmt.Errorf("probe: 不支持的来源类型 %s", kind)
	}
}

// seasonHint answers the question a single-video link raises without acting on
// it: this video is one episode of a 合集, and the whole collection is one paste
// away. It returns "" for a video in no 合集, which is the common case.
func seasonHint(infos []*bili.VideoInfo) string {
	for _, vi := range infos {
		season := vi.UgcSeason
		if season == nil || season.ID == 0 || season.Mid == 0 {
			continue
		}
		return fmt.Sprintf("提示：这条视频属于合集《%s》，共 %d 条。要抓整个合集，用这个链接：\n  %s",
			season.Title, season.VideoCount(),
			pipeline.SeasonURL(strconv.FormatInt(season.Mid, 10), strconv.FormatInt(season.ID, 10)))
	}
	return ""
}

// expandPages writes one store.Video per page (Ruling F): a multi-P video is
// probed and downloaded page by page, so each page needs its own row. Videos
// without a pages list are a single page using the video-level cid.
func expandPages(vi *bili.VideoInfo, sourceKey string) []store.Video {
	charging := vi.IsUpowerExclusive || vi.Rights.IsUpowerExclusive || vi.IsUpowerPlay || vi.IsUpowerPreview
	pages := vi.Pages
	if len(pages) == 0 {
		pages = []bili.PageInfo{{Cid: vi.Cid, Page: 1}}
	}
	out := make([]store.Video, 0, len(pages))
	for _, p := range pages {
		out = append(out, store.Video{
			Bvid:       vi.Bvid,
			Cid:        p.Cid,
			Aid:        vi.Aid,
			Author:     vi.Owner.Name,
			Title:      vi.Title,
			Pubdate:    vi.Pubdate,
			Duration:   vi.Duration,
			PageIndex:  p.Page,
			Part:       p.Part,
			IsCharging: charging,
			State:      store.StatePending,
			SourceKey:  sourceKey,
		})
	}
	return out
}
