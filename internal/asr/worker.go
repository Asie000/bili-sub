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
// could not transcribe the file.
type transcribeResponse struct {
	Segments []ir.Segment `json:"segments"`
	Error    string       `json:"error"`
}

// transcribe writes one request line and reads the matching response line. The
// blocking read runs in a goroutine so a cancelled ctx returns promptly even
// when the worker never replies; that goroutine is left blocked on the stream
// and finishes by itself once the pipe closes (FasterWhisper.Close kills the
// child, which unblocks it).
func (wk *worker) transcribe(ctx context.Context, audioPath string, opts Options) ([]ir.Segment, error) {
	req := transcribeRequest{Audio: audioPath, Lang: opts.Language, Model: opts.Model}
	if err := wk.enc.Encode(req); err != nil {
		return nil, fmt.Errorf("asr: 写入转写请求失败: %w", err)
	}

	type result struct {
		segments []ir.Segment
		err      error
	}
	ch := make(chan result, 1) // buffered: the reader never blocks on a gone caller
	go func() {
		var resp transcribeResponse
		if err := wk.dec.Decode(&resp); err != nil {
			ch <- result{err: fmt.Errorf("asr: 读取转写响应失败: %w", err)}
			return
		}
		if resp.Error != "" {
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
