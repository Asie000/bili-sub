package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/export"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
)

// newDownloader is a variable so tests can point the downloader at fake
// servers without touching the network; production always uses the real
// constructor.
var newDownloader = pipeline.NewDownloader

func newDownloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download",
		Short: "下载可下字幕（--transcribe 接入本地 ASR）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			lang, err := cmd.Flags().GetString("lang")
			if err != nil {
				return err
			}
			format, err := cmd.Flags().GetString("format")
			if err != nil {
				return err
			}
			transcribe, err := cmd.Flags().GetBool("transcribe")
			if err != nil {
				return err
			}
			source, err := cmd.Flags().GetString("source")
			if err != nil {
				return err
			}
			asrLang, err := cmd.Flags().GetString("asr-lang")
			if err != nil {
				return err
			}
			model, err := cmd.Flags().GetString("model")
			if err != nil {
				return err
			}
			formats, err := parseFormats(format)
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.download(cmd.Context(), cmd.OutOrStdout(), lang, formats, transcribe, asrLang, model, source, nil)
			return err
		},
	}
	cmd.Flags().String("lang", "", "字幕语言（如 zh、en），空则自动选择")
	cmd.Flags().String("format", "srt", "落盘格式，逗号分隔：srt,txt,md,json")
	cmd.Flags().Bool("transcribe", false, "对无字幕视频启用本地 ASR 转写")
	cmd.Flags().String("source", "", "只下载该来源（source_key）的可下载字幕，空则处理全部")
	cmd.Flags().String("asr-lang", defaultASRLang, "配合 --transcribe 的转写语言：zh 或 en（空则自动检测）")
	cmd.Flags().String("model", "", "配合 --transcribe 的 Whisper 模型名（空则用 worker 默认）")
	return cmd
}

// downloadStats is what one download run produced, for the CLI summary line and
// the HTTP /api/download DownloadSummary. The Transcribed/TranscribeFailed
// counters stay zero unless --transcribe ran.
type downloadStats struct {
	Total            int
	Fetched          int
	NoSubtitle       int
	ChargingLocked   int
	Failed           int
	Transcribed      int
	TranscribeFailed int
	// FirstError is the first per-video download failure reason. serve passes
	// io.Discard for the CLI writer, so without carrying it on the summary the
	// browser would see a failure count with no explanation (the 412 self-help
	// message in particular never reached the page).
	FirstError string
}

// download runs the two halves of `download`: the subtitle fetch first, because
// it is quick and independent, then the ASR leg for whatever is left without a
// subtitle. The subtitle half keeps a half-downloaded row downloadable; the ASR
// half only ever sees no_subtitle rows, so the two never contend for a video.
// legProgress reports progress for one leg of a multi-leg run. The subtitle leg
// and the ASR leg are different operations to the user, so each tick carries the
// op it belongs to: without it the browser shows "获取字幕 3/5" while the machine
// is actually transcribing, and the ASR leg's counts overwrite the download's.
type legProgress func(op string, done, total int)

// Op names for legProgress. They are the strings the SSE progress event carries
// and the browser maps back to labels.
const (
	opDownload   = "download"
	opTranscribe = "transcribe"
)

// tagLeg binds one leg's op to a sink, and returns nil for a nil sink so the leg
// keeps its "no progress wanted" behaviour instead of ticking a stub.
func tagLeg(sink legProgress, op string) func(done, total int) {
	if sink == nil {
		return nil
	}
	return func(done, total int) { sink(op, done, total) }
}

// legProgressSink adapts the plain callback shape the CLI commands use.
func legProgressSink(fn func(done, total int)) legProgress {
	if fn == nil {
		return nil
	}
	return func(_ string, done, total int) { fn(done, total) }
}

func (r *runtime) download(ctx context.Context, out io.Writer, lang string, formats []string, transcribe bool, asrLang, model, source string, onProgress legProgress) (downloadStats, error) {
	// Normalize here rather than at each flag: the web's download seam and `run`
	// reach this through the same door, so all three get the same source keys the
	// probe wrote. Idempotent, so transcribe normalizing again is harmless.
	source = pipeline.NormalizeSourceKey(source)
	stats, err := r.downloadSubtitles(ctx, out, lang, formats, source, tagLeg(onProgress, opDownload))
	if err != nil {
		return stats, err
	}
	if !transcribe {
		return stats, nil
	}
	ts, err := r.transcribe(ctx, out, asrLang, model, source, tagLeg(onProgress, opTranscribe))
	stats.Transcribed = ts.OK
	stats.TranscribeFailed = ts.Failed
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// downloadFatal reports the failures that would repeat for every remaining
// video -- an expired session, a risk-control block, or a --lang that matches
// none of the tracks -- so download stops the leg instead of re-downloading each
// one first.
func downloadFatal(err error) bool {
	return sessionExpired(err) || riskControl(err) || errors.Is(err, pipeline.ErrNoTrackForLang)
}

func (r *runtime) downloadSubtitles(ctx context.Context, out io.Writer, lang string, formats []string, source string, onProgress func(done, total int)) (downloadStats, error) {
	var stats downloadStats
	st, err := r.store()
	if err != nil {
		return stats, err
	}

	videos, err := listDownloadable(st, source)
	if err != nil {
		return stats, err
	}
	if len(videos) == 0 {
		fmt.Fprintln(out, "没有待处理的视频；先运行 `bili-sub probe`。")
		return stats, nil
	}
	client, err := r.client()
	if err != nil {
		return stats, err
	}
	stats.Total = len(videos)
	// The progress total is known once the downloadable rows are selected; the
	// start tick fires before the first worker, and the final (total,total) tick
	// after the last one, so the page shows 0/N then N/N even around failures.
	emit := func(done, total int) {
		if onProgress != nil {
			onProgress(done, total)
		}
	}
	emit(0, stats.Total)
	tick := newProgressEmitter(stats.Total, onProgress)
	var doneCount int64
	en := newEnumerator(client)
	dir := r.subtitleDir()
	dl := newDownloader(client, st, en, dir)

	// runCtx is cancelled when the first worker sees a whole-leg failure, so the
	// other in-flight requests stop instead of each burning a request.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	abort := newLegAbort(cancel, downloadFatal)

	results := pipeline.Run(runCtx, videos, r.cfg.Concurrency, func(ctx context.Context, v store.Video) (*ir.Document, error) {
		doc, err := dl.FetchSubtitle(ctx, v, lang)
		tick(int(atomic.AddInt64(&doneCount, 1)))
		if err == nil {
			return doc, nil
		}
		// An expired session is about the login, not this video: stop the whole
		// run and leave every row's state alone (spec §八).
		if abort.hit(err) {
			return nil, err
		}
		// "no subtitle" and "charging-locked" are classifications, not failures:
		// the download step is where they are now discovered, so it records the
		// state and leaves the run going instead of marking the row failed.
		if ctx.Err() == nil {
			switch {
			case errors.Is(err, pipeline.ErrNoSubtitle):
				if serr := st.SetState(v.ID, store.StateNoSubtitle, ""); serr != nil {
					err = errors.Join(err, fmt.Errorf("标记 no_subtitle: %w", serr))
				}
			case errors.Is(err, pipeline.ErrChargingLocked):
				if serr := st.SetState(v.ID, store.StateChargingLocked, ""); serr != nil {
					err = errors.Join(err, fmt.Errorf("标记 charging_locked: %w", serr))
				}
			default:
				if serr := st.SetState(v.ID, store.StateFailed, v.Lang); serr != nil {
					err = errors.Join(err, fmt.Errorf("标记 failed: %w", serr))
				}
			}
		}
		return nil, err
	})

	var okCount, failCount int
	for i, res := range results {
		v := videos[i]
		if res.Err != nil || res.Value == nil {
			// A cancelled run leaves every item it never reached with ctx.Err(),
			// and the -101 that stopped the run is reported once below. Neither is
			// a failure of this video.
			if errors.Is(res.Err, context.Canceled) || downloadFatal(res.Err) {
				continue
			}
			// Classifications, not failures: the download step is where "no
			// subtitle" and "charging-locked" are discovered now.
			if errors.Is(res.Err, pipeline.ErrNoSubtitle) {
				stats.NoSubtitle++
				continue
			}
			if errors.Is(res.Err, pipeline.ErrChargingLocked) {
				stats.ChargingLocked++
				continue
			}
			failCount++
			if res.Err != nil {
				if stats.FirstError == "" {
					stats.FirstError = res.Err.Error()
				}
				fmt.Fprintf(out, "第 %d 项 %s 失败：%v\n", i+1, v.Bvid, res.Err)
			}
			continue
		}
		if err := writeSubtitleFormats(dir, v, res.Value, formats); err != nil {
			return stats, fmt.Errorf("download %s: %w", v.Bvid, err)
		}
		okCount++
		fmt.Fprintf(out, "第 %d 项 %s 已下载（%s）\n", i+1, v.Bvid, res.Value.Lang)
	}
	stats.Fetched = okCount
	stats.Failed = failCount
	if aerr := abort.aborted(); aerr != nil {
		var msg string
		if riskControl(aerr) {
			msg = "请求被 B站风控限流，已停止下载（" + aerr.Error() + "）；剩余视频保持原状态，等几分钟再重试。"
		} else if errors.Is(aerr, pipeline.ErrNoTrackForLang) {
			msg = "指定的字幕语言在这些视频里没有匹配的轨道，已停止下载（" + aerr.Error() + "）；剩余视频保持原状态。"
		} else {
			msg = "登录态失效，已停止下载（" + aerr.Error() + "）。请先运行 `bili-sub login`；剩余视频保持原状态。"
		}
		fmt.Fprintln(out, msg)
		stats.FirstError = msg
		return stats, aerr
	}
	if err := ctx.Err(); err != nil {
		// Reporting "下载完成：共 100，成功 2，失败 98" for a run the user stopped
		// describes failures that did not happen, and the final tick would push
		// the web progress bar to 100%. The rows are untouched (the per-item
		// state writes are guarded on ctx), so say what actually happened.
		fmt.Fprintf(out, "已取消：完成 %d/%d，其余视频保持原状态。\n", okCount, stats.Total)
		return stats, err
	}
	emit(stats.Total, stats.Total)
	fmt.Fprintf(out, "下载完成：共 %d，成功 %d，无字幕 %d，充电专属 %d，失败 %d，输出目录 %s\n",
		stats.Total, okCount, stats.NoSubtitle, stats.ChargingLocked, failCount, dir)
	return stats, nil
}

// listDownloadable collects the rows the download step owns: freshly listed
// rows (pending) plus rows an earlier version of the pipeline already marked
// downloadable. Listing and the subtitle check used to be one step; now a row is
// pending until download classifies it (fetched / no_subtitle / charging_locked
// / failed). The merge keeps a database written by the old version usable.
func listDownloadable(st *store.Store, source string) ([]store.Video, error) {
	var videos []store.Video
	seen := map[int64]bool{}
	for _, state := range []string{store.StatePending, store.StateDownloadable} {
		list, err := st.ListVideosFiltered(state, source)
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			if seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			videos = append(videos, v)
		}
	}
	return videos, nil
}

// writeSubtitleFormats renders the requested formats next to the .srt that
// FetchSubtitle already wrote. srt is skipped: the downloader owns that file.
// The files land flat in OutputDir with the same UP主-prefixed stem the
// downloader used, so the on-disk layout matches the export zip.
func writeSubtitleFormats(dir string, v store.Video, doc *ir.Document, formats []string) error {
	stem := pageStem(v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, format := range formats {
		if format == export.FormatSRT {
			continue
		}
		body, err := export.Render(doc, format)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, stem+"."+format), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}
