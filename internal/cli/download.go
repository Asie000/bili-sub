package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/asr"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
)

// transcribeWorkerScript is the faster-whisper worker the CLI asks Python to
// run. It is resolved relative to the working directory, where `go run` and the
// built binary are expected to be started from.
const transcribeWorkerScript = "scripts/transcribe_worker.py"

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
			formats, err := parseFormats(format)
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.download(cmd.Context(), cmd.OutOrStdout(), lang, formats, transcribe)
		},
	}
	cmd.Flags().String("lang", "", "字幕语言（如 zh、en），空则自动选择")
	cmd.Flags().String("format", "srt", "落盘格式，逗号分隔：srt,txt,md,json")
	cmd.Flags().Bool("transcribe", false, "为无字幕视频启用本地 ASR（v1 尚无音频源，会如实提示跳过）")
	return cmd
}

func (r *runtime) download(ctx context.Context, out io.Writer, lang string, formats []string, transcribe bool) error {
	st, err := r.store()
	if err != nil {
		return err
	}

	if transcribe {
		// Task 15 wires the pluggable ASR seam but deliberately stops short of
		// fetching play audio. Without an audio source there is nothing to
		// transcribe, so --transcribe says so instead of pretending to work.
		backend := asr.NewFasterWhisper(r.cfg.PythonPath, transcribeWorkerScript, "")
		defer backend.Close()
		fmt.Fprintf(out, "本地 ASR 后端：%s（Python=%s，脚本=%s）。\n",
			backend.Name(), r.cfg.PythonPath, transcribeWorkerScript)
		noSubtitle, err := st.ListVideos(store.StateNoSubtitle)
		if err != nil {
			return err
		}
		if len(noSubtitle) > 0 {
			fmt.Fprintf(out, "注意：%d 个无字幕视频需要本地转写，但 v1 尚未下载播放音频（缺少音频源），本次跳过、不转写。\n",
				len(noSubtitle))
		}
	}

	videos, err := st.ListVideos(store.StateDownloadable)
	if err != nil {
		return err
	}
	if len(videos) == 0 {
		fmt.Fprintln(out, "没有可下载的字幕；先运行 probe。")
		return nil
	}
	client, err := r.client()
	if err != nil {
		return err
	}
	en := pipeline.NewEnumerator(client)
	dir := r.subtitleDir()
	dl := pipeline.NewDownloader(client, st, en, dir)

	results := pipeline.Run(ctx, videos, r.cfg.Concurrency, func(ctx context.Context, v store.Video) (*ir.Document, error) {
		doc, err := dl.FetchSubtitle(ctx, v, lang)
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
			return fmt.Errorf("download %s: %w", v.Bvid, err)
		}
		okCount++
		fmt.Fprintf(out, "第 %d 项 %s 已下载（%s）\n", i+1, v.Bvid, res.Value.Lang)
	}
	fmt.Fprintf(out, "下载完成：共 %d，成功 %d，失败 %d，输出目录 %s\n", len(videos), okCount, failCount, dir)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// writeSubtitleFormats renders the requested formats next to the .srt that
// FetchSubtitle already wrote. srt is skipped: the downloader owns that file.
func writeSubtitleFormats(dir string, v store.Video, doc *ir.Document, formats []string) error {
	stem := pageStem(v)
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

func newTranscribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "transcribe",
		Short: "对无字幕视频转写（v1 尚无音频源）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "transcribe: 本地 ASR 后端已接入，但 v1 尚未下载播放音频，暂无法转写无字幕视频。")
			return nil
		},
	}
}
