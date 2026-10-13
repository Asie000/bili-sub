package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
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

// newEnumerator and newProber are variables so tests can point the probe
// pipeline at fake servers; production always uses the real constructors. This
// mirrors the newQRLoginClient seam in serve.go.
var (
	newEnumerator = pipeline.NewEnumerator
	newProber     = pipeline.NewProber
)

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
			force, err := cmd.Flags().GetBool("force")
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.probe(cmd.Context(), cmd.OutOrStdout(), args[0], "", force, nil)
			return err
		},
	}
	cmd.Flags().Bool("force", false, "重新探测已完成的视频（fetched/transcribed/exported）")
	return cmd
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
}

// probeOutcome is what one worker learned about one video. err is carried in
// the value (not as Run's error) so a per-video failure is distinguishable from
// a cancelled run, which Run reports through Result.Err. skipped marks a video
// that was already completed before this run and so was deliberately not probed.
type probeOutcome struct {
	result  *pipeline.ProbeResult
	err     error
	skipped bool
}

func (r *runtime) probe(ctx context.Context, out io.Writer, rawURL, kindSpec string, force bool, onProgress func(done, total int)) (probeStats, error) {
	var stats probeStats
	var kind pipeline.SourceKind
	var key string
	var err error
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

	infos, err := collectVideos(ctx, en, kind, key)
	if err != nil {
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

	// A video already carried through download/transcribe/export must not be
	// probed again: re-probing would overwrite its fetched state back to
	// downloadable and (with it) discard the fact that its subtitle is stored.
	// --force re-probes everything. The states are read back from the store
	// after the upsert so a row that already existed keeps its persisted state
	// (UpsertVideo never touches state on conflict).
	completed := map[int64]string{}
	if !force {
		existing, err := st.ListVideosFiltered("", key)
		if err != nil {
			return stats, err
		}
		for _, v := range existing {
			switch v.State {
			case store.StateFetched, store.StateTranscribed, store.StateExported:
				completed[v.ID] = v.State
			}
		}
	}

	// Report progress from the workers. The emitter is shared by every worker
	// goroutine, so its throttle guard and the counter are synchronized; the
	// callback itself runs outside the lock so a slow SSE broadcast cannot
	// serialize the workers.
	var doneCount int64
	if onProgress != nil {
		onProgress(0, stats.Total)
	}
	tick := newProgressEmitter(stats.Total, onProgress)

	prober := newProber(client, st, en)
	outcomes := pipeline.Run(ctx, videos, r.cfg.Concurrency, func(ctx context.Context, v store.Video) (probeOutcome, error) {
		if _, skip := completed[v.ID]; skip {
			tick(int(atomic.AddInt64(&doneCount, 1)))
			return probeOutcome{skipped: true}, nil
		}
		res, err := prober.ProbeVideo(ctx, v)
		tick(int(atomic.AddInt64(&doneCount, 1)))
		if err == nil {
			return probeOutcome{result: res}, nil
		}
		// A failing video must not abort the rest of the run. A cancelled run
		// is not a per-video failure, so it is left for `retry` to redo.
		if ctx.Err() == nil {
			if serr := st.SetState(v.ID, store.StateFailed, v.Lang); serr != nil {
				err = errors.Join(err, fmt.Errorf("标记 failed: %w", serr))
			}
		}
		return probeOutcome{err: err}, nil
	})

	stateFor := func(i int, v store.Video) string {
		o := outcomes[i]
		switch {
		case o.Value.skipped:
			stats.Skipped++
			return "已完成"
		case o.Value.result != nil && o.Value.result.ChargingLocked:
			stats.ChargingLocked++
			return "充电专属"
		case o.Value.result != nil && o.Value.result.HasSubtitle:
			stats.Downloadable++
			return "可下载"
		case o.Value.result != nil:
			stats.NoSubtitle++
			return "无字幕"
		default:
			// Either ProbeVideo failed or the run was cancelled before this
			// video was picked up.
			stats.Failed++
			return "失败"
		}
	}
	langFor := func(i int, v store.Video) string {
		res := outcomes[i].Value.result
		if res == nil || !res.HasSubtitle {
			return ""
		}
		if best, ok := pipeline.PickLang(res.Langs, ""); ok {
			return best.Lan
		}
		return ""
	}
	if err := writeVideosTable(out, videos, stateFor, langFor); err != nil {
		return stats, err
	}
	fmt.Fprintf(out, "探测完成：共 %d，可下载 %d，无字幕 %d，充电锁定 %d，失败 %d，已完成 %d\n",
		stats.Total, stats.Downloadable, stats.NoSubtitle, stats.ChargingLocked, stats.Failed, stats.Skipped)
	// The table shows only a 失败 marker, so print why each probe failed.
	for i, o := range outcomes {
		if o.Value.err != nil {
			fmt.Fprintf(out, "第 %d 项 %s 失败：%v\n", i+1, videos[i].Bvid, o.Value.err)
		}
	}
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	return stats, nil
}

// collectVideos turns a parsed source into full VideoInfo records.
func collectVideos(ctx context.Context, en *pipeline.Enumerator, kind pipeline.SourceKind, key string) ([]*bili.VideoInfo, error) {
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
