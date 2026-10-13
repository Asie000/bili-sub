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
	Failed           int
	Transcribed      int
	TranscribeFailed int
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

func (r *runtime) downloadSubtitles(ctx context.Context, out io.Writer, lang string, formats []string, source string, onProgress func(done, total int)) (downloadStats, error) {
	var stats downloadStats
	st, err := r.store()
	if err != nil {
		return stats, err
	}

	videos, err := st.ListVideosFiltered(store.StateDownloadable, source)
	if err != nil {
		return stats, err
	}
	if len(videos) == 0 {
		fmt.Fprintln(out, "没有可下载的字幕；先运行 probe。")
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

	results := pipeline.Run(ctx, videos, r.cfg.Concurrency, func(ctx context.Context, v store.Video) (*ir.Document, error) {
		doc, err := dl.FetchSubtitle(ctx, v, lang)
		tick(int(atomic.AddInt64(&doneCount, 1)))
		if err == nil {
			return doc, nil
		}
		// Keep the run going; a failed download is retryable after `retry`.
		if ctx.Err() == nil {
			if serr := st.SetState(v.ID, store.StateFailed, v.Lang); serr != nil {
				err = errors.Join(err, fmt.Errorf("标记 failed: %w", serr))
			}
		}
		return nil, err
	})

	var okCount, failCount int
	for i, res := range results {
		v := videos[i]
		if res.Err != nil || res.Value == nil {
			failCount++
			if res.Err != nil {
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
	emit(stats.Total, stats.Total)
	stats.Fetched = okCount
	stats.Failed = failCount
	fmt.Fprintf(out, "下载完成：共 %d，成功 %d，失败 %d，输出目录 %s\n", stats.Total, okCount, failCount, dir)
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	return stats, nil
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
