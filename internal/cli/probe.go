package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
)

func newProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "probe <url>",
		Short: "枚举来源并探测字幕可用性（不下载）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.probe(cmd.Context(), cmd.OutOrStdout(), args[0])
			return err
		},
	}
}

// probeStats is what one probe run learned about its source. Both the CLI
// summary line and the HTTP /api/probe ProbeSummary are built from it.
type probeStats struct {
	Total          int
	Downloadable   int
	NoSubtitle     int
	ChargingLocked int
	Failed         int
}

// probeOutcome is what one worker learned about one video. err is carried in
// the value (not as Run's error) so a per-video failure is distinguishable from
// a cancelled run, which Run reports through Result.Err.
type probeOutcome struct {
	result *pipeline.ProbeResult
	err    error
}

func (r *runtime) probe(ctx context.Context, out io.Writer, rawURL string) (probeStats, error) {
	var stats probeStats
	kind, key, err := pipeline.ParseURL(rawURL)
	if err != nil {
		return stats, err
	}
	client, err := r.client()
	if err != nil {
		return stats, err
	}
	en := pipeline.NewEnumerator(client)

	infos, err := collectVideos(ctx, en, kind, key)
	if err != nil {
		return stats, err
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

	prober := pipeline.NewProber(client, st, en)
	outcomes := pipeline.Run(ctx, videos, r.cfg.Concurrency, func(ctx context.Context, v store.Video) (probeOutcome, error) {
		res, err := prober.ProbeVideo(ctx, v)
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
	fmt.Fprintf(out, "探测完成：共 %d，可下载 %d，无字幕 %d，充电锁定 %d，失败 %d\n",
		stats.Total, stats.Downloadable, stats.NoSubtitle, stats.ChargingLocked, stats.Failed)
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
	default:
		return nil, fmt.Errorf("probe: 不支持的来源类型 %s", kind)
	}
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
