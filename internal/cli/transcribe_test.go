package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bilibili-sub/internal/asr"
	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/pipeline"
	"bilibili-sub/internal/store"
)

// fakeASR is a scripted asr.Backend. It records the audio paths and options it
// was handed, which is how a test pins that the pipeline downloaded the audio
// and passed the chosen language/model through.
type fakeASR struct {
	segments []ir.Segment
	err      error
	// startErr, when set, makes Start fail the way a missing Python or a missing
	// faster-whisper does.
	startErr error
	// cancel, when set, is called during Transcribe so a test can cancel the run
	// in the middle of a video.
	cancel context.CancelFunc

	mu       sync.Mutex
	gotPaths []string
	gotOpts  []asr.Options
	started  bool
	closed   bool
}

// Start records that the backend was readied before the per-video loop.
func (f *fakeASR) Start() error {
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return f.startErr
}

func (f *fakeASR) Transcribe(_ context.Context, audioPath string, opts asr.Options) ([]ir.Segment, error) {
	f.mu.Lock()
	f.gotPaths = append(f.gotPaths, audioPath)
	f.gotOpts = append(f.gotOpts, opts)
	cancel, err, segments := f.cancel, f.err, f.segments
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return nil, err
	}
	return segments, nil
}

func (f *fakeASR) Name() string { return "fake-asr" }

func (f *fakeASR) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeASR) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gotPaths...)
}

// fakeNavBody is the nav response both fake servers answer with: it carries the
// two 32-character wbi keys the signer needs.
const fakeNavBody = `{"code":0,"message":"0","data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png","sub_url":"https://i0.hdslb.com/bfs/wbi/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}}}`

// newAudioServer serves the fingerprint/nav/playurl endpoints plus the audio
// body itself, so one httptest server stands in for both the API and the CDN.
func newAudioServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/frontend/finger/spi":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"b_3":"B3","b_4":"B4"}}`)
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, fakeNavBody)
		case "/x/player/wbi/playurl":
			fmt.Fprintf(w, `{"code":0,"message":"0","data":{"dash":{"audio":[{"baseUrl":"http://%s/audio/a.m4s","bandwidth":30216}],"duration":60}}}`, r.Host)
		case "/audio/a.m4s":
			_, _ = io.WriteString(w, "fake-audio-bytes")
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newFailingPlayurlServer serves the fingerprint and nav endpoints normally and
// answers playurl with one API error code, so a test can pin how the pipeline
// reacts to an expired session (-101) versus a rejected request (-400).
func newFailingPlayurlServer(t *testing.T, code int, message string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/frontend/finger/spi":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"b_3":"B3","b_4":"B4"}}`)
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, fakeNavBody)
		case "/x/player/wbi/playurl":
			fmt.Fprintf(w, `{"code":%d,"message":%q}`, code, message)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// overrideAudioPipeline points the enumerator (nav, fingerprint) and the audio
// fetcher's playurl endpoint at one fake server.
func overrideAudioPipeline(t *testing.T, base string) {
	t.Helper()
	prevEn, prevAF := newEnumerator, newAudioFetcher
	newEnumerator = func(c *bili.Client) *pipeline.Enumerator {
		c.FingerprintURL = base + "/x/frontend/finger/spi"
		en := pipeline.NewEnumerator(c)
		en.NavURL = base + "/x/web-interface/nav"
		en.ViewBaseURL = base + "/x/web-interface/view"
		return en
	}
	newAudioFetcher = func(c *bili.Client, en *pipeline.Enumerator, dir string) *pipeline.AudioFetcher {
		af := pipeline.NewAudioFetcher(c, en, dir)
		af.PlayURLBaseURL = base + "/x/player/wbi/playurl"
		return af
	}
	t.Cleanup(func() { newEnumerator, newAudioFetcher = prevEn, prevAF })
}

// overrideASRBackend replaces the production faster-whisper backend and
// captures the model it was constructed with.
func overrideASRBackend(t *testing.T, backend asr.Backend, gotModel *string) {
	t.Helper()
	prev := newASRBackend
	newASRBackend = func(_, _ string, model string) asr.Backend {
		if gotModel != nil {
			*gotModel = model
		}
		return backend
	}
	t.Cleanup(func() { newASRBackend = prev })
}

// seedVideoRow inserts one video into the data dir's database.
func seedVideoRow(t *testing.T, dir string, v store.Video) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertVideo(&v); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
}

// executeOut runs the command tree and returns its standard output.
func executeOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return executeContextOut(t, context.Background(), args...)
}

// executeContextOut runs the command tree under a caller-supplied context, so a
// test can cancel a run while a video is in flight.
func executeContextOut(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

// onlyVideo returns the single video row in the data dir's database.
func onlyVideo(t *testing.T, dir string) store.Video {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	videos, err := st.ListVideos("")
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if len(videos) != 1 {
		t.Fatalf("videos = %d, want exactly one row", len(videos))
	}
	return videos[0]
}

// TestTranscribeWritesSRTAndMarksVideoTranscribed drives the whole ASR leg: a
// no-subtitle video is turned into a downloaded audio file, a transcript row,
// an .srt on disk and the transcribed state, and the temporary audio is removed.
func TestTranscribeWritesSRTAndMarksVideoTranscribed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	backend := &fakeASR{segments: []ir.Segment{
		{StartMs: 0, EndMs: 1200, Text: "你好"},
		{StartMs: 1200, EndMs: 2400, Text: "世界"},
	}}
	var gotModel string
	overrideASRBackend(t, backend, &gotModel)

	out, err := executeOut(t, "transcribe", "--model", "large-v3-turbo")
	if err != nil {
		t.Fatalf("transcribe: %v\n%s", err, out)
	}
	if gotModel != "large-v3-turbo" {
		t.Errorf("backend model = %q, want the --model flag value", gotModel)
	}

	paths := backend.paths()
	if len(paths) != 1 {
		t.Fatalf("backend paths = %v, want exactly one transcription", paths)
	}
	if !strings.HasSuffix(paths[0], ".m4s") {
		t.Errorf("audio path = %q, want the downloaded dash file", paths[0])
	}
	if !backend.closed {
		t.Error("backend was not closed")
	}

	srtPath := filepath.Join(dir, "subtitles", "某UP - 无字幕视频 [BV1nosubtitle].srt")
	body, err := os.ReadFile(srtPath)
	if err != nil {
		t.Fatalf("read srt: %v", err)
	}
	if !strings.Contains(string(body), "你好") || !strings.Contains(string(body), "00:00:01,200 --> 00:00:02,400") {
		t.Errorf("srt = %q, want the transcribed segments with timestamps", body)
	}

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	videos, err := st.ListVideos("")
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if len(videos) != 1 || videos[0].State != store.StateTranscribed || videos[0].Lang != "zh" {
		t.Errorf("video = %+v, want state=transcribed lang=zh", videos[0])
	}
	engine, lang, irJSON, err := st.GetTranscript(videos[0].ID)
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	if engine != "fake-asr" || lang != "zh" {
		t.Errorf("transcript engine/lang = %q/%q, want fake-asr/zh", engine, lang)
	}
	if !strings.Contains(irJSON, "你好") {
		t.Errorf("stored IR = %q, want the transcribed text", irJSON)
	}

	audioDir := filepath.Join(dir, "audio")
	if entries, err := os.ReadDir(audioDir); err != nil && !os.IsNotExist(err) {
		t.Fatalf("read audio dir: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("audio dir = %v, want the temporary audio removed", entries)
	}
}

// TestTranscribeMarksFailedLeavesNoAudio pins the failure path: a backend error
// marks the video failed, still removes the temporary audio, and does not leave
// a half-written subtitle behind.
func TestTranscribeMarksFailedLeavesNoAudio(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	overrideASRBackend(t, &fakeASR{err: errors.New("模型未安装")}, nil)

	if _, err := executeOut(t, "transcribe"); err != nil {
		t.Fatalf("transcribe must not fail the whole run for one bad video: %v", err)
	}

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	videos, err := st.ListVideos("")
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if len(videos) != 1 || videos[0].State != store.StateFailed {
		t.Errorf("video = %+v, want state=failed", videos[0])
	}
	if _, _, _, err := st.GetTranscript(videos[0].ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("transcript lookup err = %v, want no transcript row", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "audio"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read audio dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("audio dir = %v, want the temporary audio removed", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "subtitles", "某UP - 无字幕视频 [BV1nosubtitle].srt")); !os.IsNotExist(err) {
		t.Errorf("srt must not exist after a failed transcription, stat err = %v", err)
	}
}

// TestDownloadTranscribeAlsoTranscribes pins the flag wiring: `download
// --transcribe` runs the ASR leg even when there is nothing to download, which
// is exactly the no-subtitle case the flag exists for.
func TestDownloadTranscribeAlsoTranscribes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 88, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	backend := &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 900, Text: "转写内容"}}}
	overrideASRBackend(t, backend, nil)

	out, err := executeOut(t, "download", "--transcribe")
	if err != nil {
		t.Fatalf("download --transcribe: %v\n%s", err, out)
	}
	if len(backend.paths()) != 1 {
		t.Fatalf("backend paths = %v, want the no-subtitle video to be transcribed", backend.paths())
	}

	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	videos, err := st.ListVideos("")
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if len(videos) != 1 || videos[0].State != store.StateTranscribed {
		t.Errorf("video = %+v, want state=transcribed", videos[0])
	}
}

// TestTranscribePassesLanguageToBackend pins that the chosen ASR language
// reaches the backend instead of being silently ignored.
func TestTranscribePassesLanguageToBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 99, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	backend := &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 500, Text: "hello"}}}
	overrideASRBackend(t, backend, nil)

	if _, err := executeOut(t, "transcribe", "--lang", "en"); err != nil {
		t.Fatalf("transcribe --lang en: %v", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.gotOpts) != 1 || backend.gotOpts[0].Language != "en" {
		t.Errorf("backend opts = %+v, want Language=en", backend.gotOpts)
	}
}

// TestTranscribeAbortsWhenSessionIsInvalid pins the spec's §八 rule: -101 means
// the session is bad, which is a whole-leg failure, not a per-video one. The run
// must stop with a message instead of spending one signed request per video and
// flipping every no_subtitle row to failed.
func TestTranscribeAbortsWhenSessionIsInvalid(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newFailingPlayurlServer(t, -101, "账号未登录").URL)
	backend := &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 100, Text: "不该发生"}}}
	overrideASRBackend(t, backend, nil)

	out, err := executeOut(t, "transcribe")
	if err == nil {
		t.Fatalf("want an error when the session is invalid\n%s", out)
	}
	if len(backend.paths()) != 0 {
		t.Errorf("backend paths = %v, want no transcription attempted", backend.paths())
	}
	if got := onlyVideo(t, dir).State; got != store.StateNoSubtitle {
		t.Errorf("state = %q, want the row left untouched so a later login can retry it", got)
	}
}

// TestTranscribeAbortsWhenBackendCannotStart pins the other whole-leg failure:
// with no Python or no faster-whisper, the engine is readied once before the
// loop so the run stops instead of marking every video failed.
func TestTranscribeAbortsWhenBackendCannotStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	backend := &fakeASR{startErr: errors.New("python 未安装")}
	overrideASRBackend(t, backend, nil)

	out, err := executeOut(t, "transcribe")
	if err == nil {
		t.Fatalf("want an error when the engine cannot start\n%s", out)
	}
	if !backend.started {
		t.Error("backend Start was not called before the per-video loop")
	}
	if len(backend.paths()) != 0 {
		t.Errorf("backend paths = %v, want no transcription attempted", backend.paths())
	}
	if got := onlyVideo(t, dir).State; got != store.StateNoSubtitle {
		t.Errorf("state = %q, want the row left untouched", got)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "audio")); err == nil && len(entries) != 0 {
		t.Errorf("audio dir = %v, want nothing downloaded for a worker that never started", entries)
	}
}

// TestTranscribeKeepsARejectedVideoAsAPerVideoFailure pins the other half of the
// -101 abort rule: -400 is bilibili rejecting that one request, not an expired
// session, so the run must keep going and only that video ends up failed.
func TestTranscribeKeepsARejectedVideoAsAPerVideoFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newFailingPlayurlServer(t, -400, "请求错误").URL)
	backend := &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 100, Text: "不该发生"}}}
	overrideASRBackend(t, backend, nil)

	out, err := executeOut(t, "transcribe")
	if err != nil {
		t.Fatalf("a rejected request is a per-video failure, not a run-level one: %v\n%s", err, out)
	}
	if len(backend.paths()) != 0 {
		t.Errorf("backend paths = %v, want none: the audio fetch failed", backend.paths())
	}
	if got := onlyVideo(t, dir).State; got != store.StateFailed {
		t.Errorf("state = %q, want failed", got)
	}
}

// TestTranscribeDoesNotMarkFailedWhenCancelledMidVideo pins both halves of the
// cancellation contract: the in-flight row keeps its state (a cancelled run is
// not a per-video failure), and the caller still sees the cancellation -- a
// cancelled run must not look like a clean one, which is what downloadSubtitles
// and probe do with the same trailing guard.
func TestTranscribeDoesNotMarkFailedWhenCancelledMidVideo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeASR{err: errors.New("转写被取消"), cancel: cancel}
	overrideASRBackend(t, backend, nil)

	out, err := executeContextOut(t, ctx, "transcribe")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled so a cancelled run cannot report success\n%s", err, out)
	}
	if got := onlyVideo(t, dir).State; got != store.StateNoSubtitle {
		t.Errorf("state = %q, want the video untouched by a cancelled run", got)
	}
}

// TestTranscribeEmptyTranscriptIsAFailure pins the silent-video branch: a model
// that recognises nothing must not be recorded as a successful transcription
// with an empty subtitle.
func TestTranscribeEmptyTranscriptIsAFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	overrideASRBackend(t, &fakeASR{segments: nil}, nil)

	out, err := executeOut(t, "transcribe")
	if err != nil {
		t.Fatalf("one silent video must not fail the whole run: %v\n%s", err, out)
	}

	v := onlyVideo(t, dir)
	if v.State != store.StateFailed {
		t.Errorf("state = %q, want failed for an empty transcript", v.State)
	}
	st, err := store.Open(filepath.Join(dir, "bili_sub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if _, _, _, err := st.GetTranscript(v.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("transcript lookup err = %v, want no transcript row", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "subtitles", "某UP - 无字幕视频 [BV1nosubtitle].srt")); !os.IsNotExist(err) {
		t.Errorf("srt must not exist for an empty transcript, stat err = %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "audio")); err == nil && len(entries) != 0 {
		t.Errorf("audio dir = %v, want the temporary audio removed", entries)
	}
}

// TestDownloadWithoutTranscribeLeavesTheAsrLegAlone is the control for
// TestDownloadTranscribeAlsoTranscribes: without the flag nothing may reach the
// ASR backend at all.
func TestDownloadWithoutTranscribeLeavesTheAsrLegAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 88, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	backend := &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 900, Text: "不该发生"}}}
	overrideASRBackend(t, backend, nil)

	out, err := executeOut(t, "download")
	if err != nil {
		t.Fatalf("download: %v\n%s", err, out)
	}
	if backend.started || len(backend.paths()) != 0 {
		t.Errorf("ASR backend was used without --transcribe (started=%t paths=%v)", backend.started, backend.paths())
	}
	if got := onlyVideo(t, dir).State; got != store.StateNoSubtitle {
		t.Errorf("state = %q, want the no-subtitle row untouched", got)
	}
}

// TestTranscribeMultiPageWritesPageSuffix pins that the ASR leg names its file
// through pageStem: without the _P{n} suffix the second page of a multi-P video
// would silently overwrite the first page's subtitle.
func TestTranscribeMultiPageWritesPageSuffix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 77, Author: "某UP", Title: "无字幕视频", PageIndex: 2,
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	overrideASRBackend(t, &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 900, Text: "第二P"}}}, nil)

	out, err := executeOut(t, "transcribe")
	if err != nil {
		t.Fatalf("transcribe: %v\n%s", err, out)
	}
	want := filepath.Join(dir, "subtitles", "某UP - 无字幕视频 [BV1nosubtitle]_P2.srt")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("multi-P srt missing: %v", err)
	}
}

// transcribeModel runs one seeded no-subtitle video through `transcribe` and
// returns the model name the ASR backend was constructed with.
func transcribeModel(t *testing.T, extraArgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BILI_DATA_DIR", dir)
	seedVideoRow(t, dir, store.Video{
		Bvid: "BV1nosubtitle", Cid: 42, Author: "某UP", Title: "无字幕视频",
		State: store.StateNoSubtitle,
	})

	overrideAudioPipeline(t, newAudioServer(t).URL)
	backend := &fakeASR{segments: []ir.Segment{{StartMs: 0, EndMs: 500, Text: "内容"}}}
	var gotModel string
	overrideASRBackend(t, backend, &gotModel)

	args := append([]string{"transcribe"}, extraArgs...)
	if out, err := executeOut(t, args...); err != nil {
		t.Fatalf("transcribe %v: %v\n%s", extraArgs, err, out)
	}
	return gotModel
}

// TestTranscribeModelSelection pins the precedence: --model wins, otherwise the
// ASR_MODEL config value is used, otherwise the name is left empty so the worker
// script's own default applies. Without the middle step every run would need the
// flag spelled out.
func TestTranscribeModelSelection(t *testing.T) {
	t.Run("the ASR_MODEL config value is used when the flag is absent", func(t *testing.T) {
		t.Setenv("ASR_MODEL", "large-v3-turbo")
		if got := transcribeModel(t); got != "large-v3-turbo" {
			t.Errorf("model = %q, want the ASR_MODEL config value", got)
		}
	})

	t.Run("the flag wins over the config value", func(t *testing.T) {
		t.Setenv("ASR_MODEL", "small")
		if got := transcribeModel(t, "--model", "tiny"); got != "tiny" {
			t.Errorf("model = %q, want the --model flag to win", got)
		}
	})

	t.Run("neither set leaves the worker default in charge", func(t *testing.T) {
		t.Setenv("ASR_MODEL", "")
		if got := transcribeModel(t); got != "" {
			t.Errorf("model = %q, want empty so the worker script decides", got)
		}
	})
}
