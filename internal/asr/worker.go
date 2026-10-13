package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"bilibili-sub/internal/ir"
)

// worker speaks the JSON-lines protocol of scripts/transcribe_worker.py: one
// request object per line on the write side, one response object per line on
// the read side. It is deliberately transport-free (io.Reader/io.Writer) so it
// can be exercised with in-memory pipes and no Python process.
//
// A worker is not safe for concurrent Transcribe calls: the two callers would
// interleave request/response lines on the same stream.
type worker struct {
	enc *json.Encoder
	dec *json.Decoder
}

// newWorker wires an encoder/decoder pair to the worker's stdio streams.
func newWorker(r io.Reader, w io.Writer) *worker {
	return &worker{enc: json.NewEncoder(w), dec: json.NewDecoder(r)}
}

// transcribeRequest is one protocol line. The field names are fixed by the
// worker script (audio/lang/model).
type transcribeRequest struct {
	Audio string `json:"audio"`
	Lang  string `json:"lang"`
	Model string `json:"model"`
}

// transcribeResponse is one reply line. Error is non-empty when the script
// could not transcribe the file. Kind is "engine" when the failure is about the
// engine (a model that cannot be loaded, a missing dependency) rather than this
// audio file; empty for a per-file failure.
type transcribeResponse struct {
	Segments []ir.Segment `json:"segments"`
	Error    string       `json:"error"`
	Kind     string       `json:"kind"`
}

// ErrWorkerEngine marks a failure that says nothing about the audio file being
// transcribed: the model could not be loaded, or the worker process died. A
// caller stops the whole run on it, because every remaining video would fail
// identically -- but each of them only after its audio has been downloaded.
var ErrWorkerEngine = errors.New("asr: 引擎不可用")

// transcribe writes one request line and reads the matching response line. The
// blocking read runs in a goroutine so a cancelled ctx returns promptly even
// when the worker never replies; that goroutine is left blocked on the stream
// and finishes by itself once the pipe closes (FasterWhisper.Close kills the
// child, which unblocks it).
func (wk *worker) transcribe(ctx context.Context, audioPath string, opts Options) ([]ir.Segment, error) {
	req := transcribeRequest{Audio: audioPath, Lang: opts.Language, Model: opts.Model}
	if err := wk.enc.Encode(req); err != nil {
		return nil, fmt.Errorf("%w: 写入转写请求失败: %v", ErrWorkerEngine, err)
	}

	type result struct {
		segments []ir.Segment
		err      error
	}
	ch := make(chan result, 1) // buffered: the reader never blocks on a gone caller
	go func() {
		var resp transcribeResponse
		if err := wk.dec.Decode(&resp); err != nil {
			// A broken pipe means the worker process is gone; that is an engine
			// failure, not one video's fault.
			ch <- result{err: fmt.Errorf("%w: 读取转写响应失败: %v", ErrWorkerEngine, err)}
			return
		}
		if resp.Error != "" {
			if resp.Kind == "engine" {
				ch <- result{err: fmt.Errorf("%w: %s", ErrWorkerEngine, resp.Error)}
				return
			}
			ch <- result{err: errors.New("asr: " + resp.Error)}
			return
		}
		ch <- result{segments: resp.Segments}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.segments, r.err
	}
}
