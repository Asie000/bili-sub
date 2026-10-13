package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"bilibili-sub/internal/config"
	"bilibili-sub/internal/store"
)

// TestLiveTranscribe is an opt-in end-to-end smoke test: it drives the real
// playurl endpoint, the real audio CDN and the real faster-whisper worker. It
// is skipped unless the environment supplies the pieces the fakes normally
// stand in for:
//
//	BILI_LIVE_SESSION  path to a session.json written by `bili-sub login`
//	BILI_LIVE_PYTHON   python.exe that has faster-whisper installed
//	BILI_LIVE_BVID     a real video id
//	BILI_LIVE_CID      its cid
//	BILI_LIVE_MODEL    optional model name (default tiny, to keep the download small)
func TestLiveTranscribe(t *testing.T) {
	sessionPath := os.Getenv("BILI_LIVE_SESSION")
	pythonPath := os.Getenv("BILI_LIVE_PYTHON")
	bvid := os.Getenv("BILI_LIVE_BVID")
	cidRaw := os.Getenv("BILI_LIVE_CID")
	if sessionPath == "" || pythonPath == "" || bvid == "" || cidRaw == "" {
		t.Skip("set BILI_LIVE_SESSION, BILI_LIVE_PYTHON, BILI_LIVE_BVID and BILI_LIVE_CID")
	}
	cid, err := strconv.ParseInt(cidRaw, 10, 64)
	if err != nil {
		t.Fatalf("BILI_LIVE_CID %q: %v", cidRaw, err)
	}
	model := os.Getenv("BILI_LIVE_MODEL")
	if model == "" {
		model = "tiny"
	}

	// The worker script is resolved relative to the working directory, exactly
	// as the installed binary resolves it.
	t.Chdir("../..")

	dir := t.TempDir()
	session, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), session, 0o600); err != nil {
		t.Fatalf("copy session: %v", err)
	}

	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DBPath = filepath.Join(dir, "bili_sub.db")
	cfg.PythonPath = pythonPath
	// BILI_LIVE_SIMPLIFY=0 keeps the raw model output, so one clip can be run
	// twice to see what the Chinese cleanup actually changed.
	if os.Getenv("BILI_LIVE_SIMPLIFY") == "0" {
		cfg.ASRSimplify = false
	}
	rt := &runtime{cfg: cfg}
	defer rt.close()

	st, err := rt.store()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := st.UpsertVideo(&store.Video{
		Bvid: bvid, Cid: cid, Author: "live", Title: "live smoke test",
		State: store.StateNoSubtitle,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	start := time.Now()
	stats, err := rt.transcribe(ctx, os.Stdout, "zh", model, "", nil)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	t.Logf("transcribe took %s: %+v", time.Since(start).Round(time.Second), stats)
	if stats.OK != 1 {
		t.Fatalf("stats = %+v, want one successful transcription", stats)
	}

	srt := filepath.Join(dir, "subtitles", "live - live smoke test ["+bvid+"].srt")
	body, err := os.ReadFile(srt)
	if err != nil {
		t.Fatalf("read srt: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("srt is empty")
	}
	// BILI_LIVE_OUT keeps a copy of the transcript so a benchmark can compare
	// two models' output without re-running them.
	if out := os.Getenv("BILI_LIVE_OUT"); out != "" {
		if err := os.WriteFile(out, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
		t.Logf("copied srt to %s", out)
	}
	t.Logf("srt (%d bytes):\n%s", len(body), body)
}
