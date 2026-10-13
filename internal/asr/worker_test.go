package asr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"bilibili-sub/internal/ir"
)

// TestWorkerTranscribeRoundTrip drives the JSON-lines protocol over in-memory
// pipes: the fake peer reads one request line, pins its fields, and answers
// with one response line. No Python, no model and no ffmpeg are involved.
func TestWorkerTranscribeRoundTrip(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	wk := newWorker(clientReader, clientWriter)

	gotReq := make(chan map[string]string, 1)
	go func() {
		var req map[string]string
		if err := json.NewDecoder(serverReader).Decode(&req); err != nil {
			return
		}
		gotReq <- req
		_, _ = io.WriteString(serverWriter,
			`{"segments":[{"start_ms":0,"end_ms":1500,"text":"你好"},{"start_ms":1500,"end_ms":3000,"text":"世界"}],"error":""}`+"\n")
	}()

	segs, err := wk.transcribe(context.Background(), "/tmp/a.wav", Options{Language: "zh", Model: "small"})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	want := []ir.Segment{
		{StartMs: 0, EndMs: 1500, Text: "你好"},
		{StartMs: 1500, EndMs: 3000, Text: "世界"},
	}
	if len(segs) != len(want) {
		t.Fatalf("segments = %+v, want %+v", segs, want)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Errorf("segment[%d] = %+v, want %+v", i, segs[i], want[i])
		}
	}

	select {
	case req := <-gotReq:
		if req["audio"] != "/tmp/a.wav" || req["lang"] != "zh" || req["model"] != "small" {
			t.Errorf("request line = %v, want audio=/tmp/a.wav lang=zh model=small", req)
		}
	case <-time.After(time.Second):
		t.Fatal("fake worker never received a request")
	}
}

// TestWorkerTranscribeErrorResponse pins the error branch: a reply whose
// "error" field is non-empty becomes a Go error carrying that message.
func TestWorkerTranscribeErrorResponse(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	wk := newWorker(clientReader, clientWriter)

	go func() {
		var req map[string]string
		if err := json.NewDecoder(serverReader).Decode(&req); err != nil {
			return
		}
		_, _ = io.WriteString(serverWriter, `{"segments":[],"error":"boom: model missing"}`+"\n")
	}()

	_, err := wk.transcribe(context.Background(), "a.wav", Options{})
	if err == nil {
		t.Fatal("transcribe: error = nil, want the worker error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %q, want it to carry the worker message", err)
	}
}

// TestWorkerTranscribeEngineError pins the `kind:"engine"` branch: a failure the
// script judged to be about the engine (a model that cannot be loaded) must be
// recognisable with errors.Is, so the caller can stop the whole run instead of
// re-downloading audio for every remaining video.
func TestWorkerTranscribeEngineError(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	wk := newWorker(clientReader, clientWriter)

	go func() {
		var req map[string]string
		if err := json.NewDecoder(serverReader).Decode(&req); err != nil {
			return
		}
		_, _ = io.WriteString(serverWriter, `{"segments":[],"error":"无法加载模型 smal","kind":"engine"}`+"\n")
	}()

	_, err := wk.transcribe(context.Background(), "a.wav", Options{Model: "smal"})
	if !errors.Is(err, ErrWorkerEngine) {
		t.Fatalf("error = %v, want it to wrap ErrWorkerEngine", err)
	}
	if !strings.Contains(err.Error(), "smal") {
		t.Fatalf("error = %q, want the worker message", err)
	}
}

// TestWorkerTranscribeContextCancel pins the blocking-read escape hatch: when
// the peer never answers, a cancelled context must return promptly instead of
// hanging on the read.
func TestWorkerTranscribeContextCancel(t *testing.T) {
	// The reader end of this pipe is never written to, so the decode blocks
	// forever; io.Discard keeps the request write non-blocking.
	pr, pw := io.Pipe()
	defer pw.Close()
	wk := newWorker(pr, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := wk.transcribe(ctx, "a.wav", Options{})
		done <- err
	}()

	// It must still be blocked before the cancel fires.
	select {
	case err := <-done:
		t.Fatalf("transcribe returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("transcribe error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transcribe did not return after the context was cancelled")
	}
}

// TestExtractAudioMissingBinary pins the graceful failure path: a nonexistent
// ffmpeg must yield a wrapped error, never a panic.
func TestExtractAudioMissingBinary(t *testing.T) {
	err := ExtractAudio(context.Background(), "definitely-not-ffmpeg-binary-xyz", "in.wav", "out.wav")
	if err == nil {
		t.Fatal("ExtractAudio with a missing binary: error = nil, want an error")
	}
}

// TestFasterWhisperDefaults covers the unstarted backend: an empty pythonPath
// falls back to "python", Name is stable, and Close on a never-started process
// is a no-op.
func TestFasterWhisperDefaults(t *testing.T) {
	f := NewFasterWhisper("", "scripts/transcribe_worker.py", "small")
	if f.pythonPath != "python" {
		t.Fatalf("pythonPath = %q, want %q", f.pythonPath, "python")
	}
	if f.Name() != "faster-whisper" {
		t.Fatalf("Name() = %q, want %q", f.Name(), "faster-whisper")
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close on an unstarted backend: %v", err)
	}
}
