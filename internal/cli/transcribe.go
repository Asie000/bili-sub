package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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

// defaultASRLang is the language handed to the model when the user does not
// choose one. The design fixes local ASR to Chinese and English, and a Chinese
// talk is the common case, so zh is the safer default than auto-detection.
const defaultASRLang = "zh"

// audioFileExt is the extension AudioFetcher writes its temporary dash audio
// with. sweepStaleAudio keys off it so it never touches anything else that
// happens to live in the scratch dir.
const audioFileExt = ".m4s"

// newAudioFetcher and newASRBackend are variables so tests can drive the ASR
// leg against fake servers and a scripted backend; production always uses the
// real constructors. This mirrors the newDownloader seam in download.go.
var (
	newAudioFetcher = pipeline.NewAudioFetcher
	newASRBackend   = func(pythonPath, scriptPath, model string) asr.Backend {
		return asr.NewFasterWhisper(pythonPath, scriptPath, model)
	}
)

func newTranscribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transcribe",
		Short: "对无字幕视频做本地 ASR 转写",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			lang, err := cmd.Flags().GetString("lang")
			if err != nil {
				return err
			}
			model, err := cmd.Flags().GetString("model")
			if err != nil {
				return err
			}
			source, err := cmd.Flags().GetString("source")
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.transcribe(cmd.Context(), cmd.OutOrStdout(), lang, model, source, nil)
			return err
		},
	}
	cmd.Flags().String("lang", defaultASRLang, "转写语言：zh 或 en（传空字符串则让模型自动检测）")
	cmd.Flags().String("model", "", "Whisper 模型名（默认取 .env 的 ASR_MODEL，两者都为空则用 worker 默认）")
	cmd.Flags().String("source", "", "只转写该来源（source_key）的无字幕视频，空则处理全部")
	return cmd
}

// transcribeStats is what one ASR run produced.
type transcribeStats struct {
	Total  int
	OK     int
	Failed int
}

// audioResult is one prefetched download. index keeps progress and messages in
// list order even though the download runs ahead of the transcription.
type audioResult struct {
	index int
	video store.Video
	path  string
	err   error
}

// prefetchDepth is how many audios may be downloaded ahead of the transcriber.
// One is enough to hide the network wait behind the GPU work; a deeper buffer
// would only pile up several-MB files, since the worker decodes one file at a
// time (its protocol is strictly one request/response).
const prefetchDepth = 2

// audioDir holds the temporary dash audio, one file per video, deleted as soon
// as its transcript is stored.
func (r *runtime) audioDir() string {
	return filepath.Join(r.cfg.DataDir, "audio")
}

// staleAudioAge is how old a leftover file must be before a run deletes it.
// The scratch dir is shared by every process pointed at the same DataDir, and the
// serialization that makes sweeping safe is per-process (runtime.opMu): two
// terminals running a pipeline at once would otherwise have one delete the file
// the other is transcribing. A file this old belongs to a run that is over -- the
// fetcher deletes each one within minutes of the video it belongs to.
const staleAudioAge = 30 * time.Minute

// sweepStaleAudio clears the temporary audio directory before a run starts.
//
// Deleting each file as soon as its transcript is stored covers every way a run
// can end except the one the OS owns: a process that is killed outright
// (SIGKILL, a closed terminal, a crash, a power cut) never reaches its defers.
// What such a run leaves is several MB per video, under a name nobody would
// connect back to a video.
func (r *runtime) sweepStaleAudio() {
	entries, err := os.ReadDir(r.audioDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != audioFileExt {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < staleAudioAge {
			continue
		}
		_ = os.Remove(filepath.Join(r.audioDir(), e.Name()))
	}
}

// transcribe runs the local ASR leg over every no_subtitle video (optionally
// scoped to one source): download the dash audio, hand it to the backend, write
// the .srt and the canonical IR, then drop the audio. One failed video does not
// stop the run; it is marked failed so `retry` can pick it up. A failure that
// says nothing about the individual video -- no usable session, no engine --
// stops the run before it mutates any state (spec §八).
func (r *runtime) transcribe(ctx context.Context, out io.Writer, lang, model, source string, onProgress func(done, total int)) (transcribeStats, error) {
	var stats transcribeStats
	// Normalized for the same reason as in download: `--source` is free text and
	// the stored search key is normalized, so "search:劳动  法" would otherwise
	// match nothing and look like "nothing to transcribe".
	source = pipeline.NormalizeSourceKey(source)
	// Anything already here is from a run that was killed before it could clean
	// up: a run that ends normally leaves nothing behind, so this only ever
	// removes garbage.
	r.sweepStaleAudio()
	st, err := r.store()
	if err != nil {
		return stats, err
	}
	videos, err := st.ListVideosFiltered(store.StateNoSubtitle, source)
	if err != nil {
		return stats, err
	}
	if len(videos) == 0 {
		fmt.Fprintln(out, "没有需要转写的无字幕视频。")
		return stats, nil
	}

	client, err := r.client()
	if err != nil {
		return stats, err
	}
	if model == "" {
		// --model > ASR_MODEL in .env > the worker script's own default. The
		// fallback lives here rather than in each command so `transcribe`,
		// `download --transcribe` and the web UI all inherit it.
		model = r.cfg.ASRModel
	}
	scriptPath := resolveRepoPath(transcribeWorkerScript)
	backend := newASRBackend(r.cfg.PythonPath, scriptPath, model)
	defer backend.Close()
	fmt.Fprintf(out, "本地 ASR 后端：%s（Python=%s，脚本=%s）\n",
		backend.Name(), r.cfg.PythonPath, scriptPath)
	// Ready the engine before touching any video. A missing Python or a missing
	// faster-whisper is a whole-leg failure, so it must stop the run instead of
	// being re-reported as one failure per video.
	if err := backend.Start(); err != nil {
		return stats, err
	}

	fetcher := newAudioFetcher(client, newEnumerator(client), r.audioDir())
	dir := r.subtitleDir()

	stats.Total = len(videos)
	if onProgress != nil {
		onProgress(0, stats.Total)
	}
	tick := newProgressEmitter(stats.Total, onProgress)

	// The audio for a video is downloaded while the previous one is being
	// transcribed: the download is network-bound and the transcription is
	// GPU-bound, so overlapping them takes the network wait off the critical
	// path. The worker still decodes one file at a time -- its protocol is one
	// request/response -- so this hides the wait without adding concurrency to
	// the GPU.
	audioCh := make(chan audioResult, prefetchDepth)
	runCtx, cancelRun := context.WithCancel(ctx)
	defer func() {
		cancelRun()
		// Whichever way this run ended, drop any audio the producer had already
		// fetched but the loop never consumed, and let it finish unwinding.
		for res := range audioCh {
			if res.err == nil {
				_ = os.Remove(res.path)
			}
		}
	}()
	go downloadAhead(runCtx, fetcher, videos, audioCh)

	for res := range audioCh {
		if err := ctx.Err(); err != nil {
			if res.err == nil {
				_ = os.Remove(res.path)
			}
			return stats, err
		}
		i, v := res.index, res.video
		err := res.err
		if err == nil {
			err = r.transcribeFetched(ctx, st, backend, dir, v, lang, res.path)
		}
		switch {
		case err == nil:
			stats.OK++
			fmt.Fprintf(out, "第 %d 项 %s 已转写（%s）\n", i+1, v.Bvid, lang)
		case sessionExpired(err):
			// An expired session (-101) is about the login, not about this video:
			// every remaining row would fail identically, so stop and leave their
			// state alone (spec §八: 判定 Cookie 失效，暂停并告警). A -400 is
			// bilibili rejecting this one request, so it stays per-video.
			fmt.Fprintf(out, "登录态失效，已停止转写（%v）。请先运行 `bili-sub login`；剩余 %d 个视频保持原状态。\n",
				err, len(videos)-i-1)
			return stats, err
		case riskControl(err):
			// Same shape as an expired session: a risk-control block is on the
			// session, so every remaining video would hit it too.
			fmt.Fprintf(out, "请求被 B站风控限流，已停止转写（%v）；剩余 %d 个视频保持原状态，等几分钟再重试。\n",
				err, len(videos)-i-1)
			return stats, err
		case errors.Is(err, asr.ErrWorkerEngine):
			// An engine failure (a model that cannot be loaded, the worker process
			// dying) would repeat identically for every remaining video -- and each
			// one only pays for that failure after downloading its full audio. Stop
			// the leg instead (spec §八).
			fmt.Fprintf(out, "ASR 引擎不可用，已停止转写（%v）；剩余 %d 个视频保持原状态。\n",
				err, len(videos)-i-1)
			return stats, err
		case ctx.Err() != nil:
			// A cancelled run is not a per-video failure: the in-flight video
			// returns context.Canceled, and counting it would report a failure that
			// never happened (downloadSubtitles and probe guard the same way).
			fmt.Fprintf(out, "已取消：完成 %d/%d，其余视频保持原状态。\n", stats.OK, stats.Total)
			return stats, ctx.Err()
		default:
			stats.Failed++
			fmt.Fprintf(out, "第 %d 项 %s 转写失败：%v\n", i+1, v.Bvid, err)
			if serr := st.SetState(v.ID, store.StateFailed, v.Lang); serr != nil {
				return stats, errors.Join(err, fmt.Errorf("标记 failed: %w", serr))
			}
		}
		tick(i + 1)
	}
	fmt.Fprintf(out, "转写完成：共 %d，成功 %d，失败 %d\n", stats.Total, stats.OK, stats.Failed)
	// A cancellation caught inside the last (or only) video never returns to the
	// loop head, so without this the run would report success and exit 0.
	// downloadSubtitles and probe carry the same trailing guard.
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

// downloadAhead fetches each video's audio in list order and hands the path to
// the transcriber over the channel. It is the network half of the pipeline: it
// runs concurrently with the GPU half, so a download is already in flight while
// the previous file is being transcribed. The channel is always closed, and the
// caller owns (and removes) every path successfully delivered.
func downloadAhead(ctx context.Context, fetcher *pipeline.AudioFetcher, videos []store.Video, ch chan<- audioResult) {
	defer close(ch)
	for i, v := range videos {
		path, err := fetcher.Fetch(ctx, v)
		select {
		case ch <- audioResult{index: i, video: v, path: path, err: err}:
		case <-ctx.Done():
			// Nobody is going to consume this file: drop it rather than leak it.
			if err == nil {
				_ = os.Remove(path)
			}
			return
		}
		if err != nil && sessionExpired(err) {
			// An expired session fails every remaining download identically, so stop
			// fetching. The result just handed over still stops the run with the
			// login message.
			return
		}
	}
}

// transcribeFetched turns one already-downloaded audio file into a stored
// transcript. It owns the file: whatever happens, it is gone when this returns.
func (r *runtime) transcribeFetched(ctx context.Context, st *store.Store, backend asr.Backend, dir string, v store.Video, lang, audioPath string) error {
	// The audio is the largest artifact in the whole pipeline and has no use
	// after this call, so it is removed on the failure path too.
	defer func() { _ = os.Remove(audioPath) }()

	segments, err := backend.Transcribe(ctx, audioPath, asr.Options{Language: lang})
	if err != nil {
		return err
	}
	// The backend returns raw model output. Cleanup lives here, on the caller's
	// side of the seam, so every backend gets the same subtitles: no special
	// tokens, no hallucination loops, and Chinese in one script.
	segments = asr.CleanSegments(segments, asr.CleanupOptions{
		Language: lang,
		Simplify: r.cfg.ASRSimplify,
	})
	if len(segments) == 0 {
		return errors.New("没有识别到语音内容")
	}

	doc := &ir.Document{
		Bvid:     v.Bvid,
		Cid:      v.Cid,
		Title:    v.Title,
		Author:   v.Author,
		Lang:     lang,
		Segments: segments,
	}
	srt, err := export.Render(doc, export.FormatSRT)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, pageStem(v)+".srt"), srt, 0o644); err != nil {
		return err
	}

	// The transcripts column is the canonical IR (with timeline), so export can
	// rebuild every format — including this .srt — without re-running the model.
	irJSON, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := st.SaveTranscript(v.ID, backend.Name(), lang, string(irJSON)); err != nil {
		return err
	}
	return st.SetState(v.ID, store.StateTranscribed, lang)
}
