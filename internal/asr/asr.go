// Package asr is the pluggable automatic-speech-recognition seam. The Go
// control plane talks to a Backend, and the v1 implementation shells out to a
// long-lived faster-whisper worker process over a JSON-lines protocol.
package asr

import (
	"context"

	"bilibili-sub/internal/ir"
)

// Options carries per-request transcription settings. An empty Language lets
// the engine auto-detect; an empty Model falls back to the backend default.
type Options struct {
	Language string
	Model    string
}

// Backend is the ASR seam. Callers depend on this interface so the local
// faster-whisper worker can later be swapped for another engine without
// touching the download/export pipeline.
type Backend interface {
	// Start readies the engine before any work is handed to it. A backend whose
	// engine is missing (no Python, no faster-whisper) fails here, which lets a
	// caller report that once instead of discovering it as a failure of every
	// single video. Calling Start on an already-started backend is a no-op.
	Start() error
	// Transcribe turns one audio file into timestamped IR segments.
	Transcribe(ctx context.Context, audioPath string, opts Options) ([]ir.Segment, error)
	// Name identifies the engine that produced a transcript (stored next to
	// the IR in the transcripts table).
	Name() string
	// Close releases any child process or resources held by the backend.
	Close() error
}
