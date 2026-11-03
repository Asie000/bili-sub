package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/pipeline"
)

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <url>",
		Short: "一条命令跑完：枚举 → 探测 → 下载 → 转写 → 导出",
		Long: `把 probe / download / transcribe / export 四个阶段按顺序跑一遍。

前三个阶段按来源（这个 URL）过滤，导出沿用 export 的规则打包所有
尚未导出的字幕。

转写阶段失败（没装 Python、登录失效）不会丢掉已经下载好的字幕：
导出照常执行，错误在最后报出来，退出码非零。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kindSpec, err := cmd.Flags().GetString("kind")
			if err != nil {
				return err
			}
			lang, err := cmd.Flags().GetString("lang")
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
			transcribe, err := cmd.Flags().GetBool("transcribe")
			if err != nil {
				return err
			}
			format, err := cmd.Flags().GetString("format")
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
			formats, err := parseFormats(format)
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.run(cmd.Context(), cmd.OutOrStdout(), args[0], kindSpec, lang, asrLang, model, transcribe, formats, force, limit, nil)
		},
	}
	cmd.Flags().String("kind", "", "来源类型（single/space/fav/season/search），空则按 URL 自动识别")
	cmd.Flags().String("lang", "", "字幕语言（如 zh、en），空则自动选择")
	cmd.Flags().Bool("transcribe", true, "对无字幕视频启用本地 ASR 转写（--transcribe=false 跳过）")
	cmd.Flags().String("asr-lang", defaultASRLang, "转写语言：zh 或 en（空则自动检测）")
	cmd.Flags().String("model", "", "Whisper 模型名（默认取 .env 的 ASR_MODEL）")
	cmd.Flags().String("format", "txt", "导出格式，逗号分隔：txt,srt,md,json（默认只导出 txt）")
	cmd.Flags().Bool("force", false, "重新探测已完成（fetched/transcribed/exported）的视频")
	cmd.Flags().Int("limit", pipeline.DefaultSearchLimit, "搜索来源最多取多少条（0 = 不限）；其他来源忽略此项")
	return cmd
}

// run is the whole pipeline in one command: exactly the stages the dedicated
// commands expose, in the order they have to happen.
//
// The ordering at the end is deliberate. The ASR leg runs before export and its
// failure is reported last, so a missing Python or an expired session cannot
// cost the user the subtitles that were already downloaded and stored: the
// archive is still written, and the non-zero exit still says something went
// wrong.
func (r *runtime) run(ctx context.Context, out io.Writer, rawURL, kindSpec, lang, asrLang, model string, transcribe bool, formats []string, force bool, limit int, onProgress func(done, total int)) error {
	// Resolve the source first: a bad link must fail before anything is written,
	// and the download leg has to be scoped to it. This mirrors what the HTTP
	// probe handler does for the same reason. A b23.tv share link is expanded to
	// its real URL first, since only that reveals the source.
	resolved, err := r.resolveInputURL(ctx, rawURL)
	if err != nil {
		return err
	}
	rawURL = resolved
	var source string
	if explicit, ok := pipeline.ParseSourceKind(kindSpec); ok {
		s, err := pipeline.ParseURLForKind(rawURL, explicit)
		if err != nil {
			return fmt.Errorf("无法解析链接: %w", err)
		}
		source = s
	} else if kindSpec != "" {
		return fmt.Errorf("未知的来源类型: %s", kindSpec)
	} else {
		_, s, err := pipeline.ParseURL(rawURL)
		if err != nil {
			return fmt.Errorf("无法解析链接: %w", err)
		}
		source = s
	}

	if _, err := r.probe(ctx, out, rawURL, kindSpec, force, limit, onProgress); err != nil {
		return err
	}

	// The two legs are driven separately here so a failure is attributed to the
	// stage that actually produced it: r.download returns one error covering
	// both the subtitle fetch and the ASR leg, which made a download-leg failure
	// look like a transcribe-leg failure.
	sink := legProgressSink(onProgress)
	source = pipeline.NormalizeSourceKey(source)
	if _, err := r.downloadSubtitles(ctx, out, lang, formats, source, tagLeg(sink, opDownload)); err != nil {
		return err
	}
	var asrErr error
	if transcribe {
		_, asrErr = r.transcribe(ctx, out, asrLang, model, source, tagLeg(sink, opTranscribe))
	}

	if _, err := r.export(ctx, out, out, formats, false, onProgress); err != nil {
		return err
	}
	if asrErr != nil {
		fmt.Fprintf(out, "\n注意：字幕已下载并导出，但转写阶段失败：%v\n", asrErr)
		return asrErr
	}
	return nil
}
