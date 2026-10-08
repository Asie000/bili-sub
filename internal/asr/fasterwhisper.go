package asr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"bilibili-sub/internal/ir"
)

// defaultPython is used when NewFasterWhisper is given an empty pythonPath.
const defaultPython = "python"

// FasterWhisper runs scripts/transcribe_worker.py as a long-lived child process
// and talks to it over the worker JSON-lines protocol. The process starts
// lazily on the first Transcribe call, so constructing a backend never fails
// just because Python or faster-whisper is absent; only Transcribe reports that.
type FasterWhisper struct {
	pythonPath string
	scriptPath string
	model      string

	mu  sync.Mutex
	cmd *exec.Cmd
	wk  *worker
}

// Compile-time proof that FasterWhisper is a Backend.
var _ Backend = (*FasterWhisper)(nil)

// NewFasterWhisper builds a backend. pythonPath defaults to "python"; model is
// the default model for requests that leave Options.Model empty.
func NewFasterWhisper(pythonPath, scriptPath, model string) *FasterWhisper {
	if pythonPath == "" {
		pythonPath = defaultPython
	}
	return &FasterWhisper{pythonPath: pythonPath, scriptPath: scriptPath, model: model}
}

// Name identifies this backend in the transcripts table.
func (f *FasterWhisper) Name() string { return "faster-whisper" }

// Transcribe starts the worker on first use, then exchanges one request/response
// pair. Calls are serialized: the protocol is a single request/response stream.
func (f *FasterWhisper) Transcribe(ctx context.Context, audioPath string, opts Options) ([]ir.Segment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wk == nil {
		if err := f.start(); err != nil {
			return nil, err
		}
	}
	if opts.Model == "" {
		opts.Model = f.model
	}
	return f.wk.transcribe(ctx, audioPath, opts)
}

// start launches `pythonPath scriptPath` and wires its stdin/stdout into a
// worker. The child's stderr is inherited so a Python traceback reaches the
// user.
func (f *FasterWhisper) start() error {
	cmd := exec.Command(f.pythonPath, f.scriptPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("asr: 无法连接 worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("asr: 无法连接 worker stdout: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("asr: 启动 %s %s 失败（需要 Python 3.9-3.12 并 pip install faster-whisper）: %w",
			f.pythonPath, f.scriptPath, err)
	}
	f.cmd = cmd
	f.wk = newWorker(stdout, stdin)
	return nil
}

// Close kills the worker process (if it was started) and waits for it to exit
// so the OS releases its handles. A killed process reports a non-nil Wait error
// on every platform, so that error is deliberately ignored; a failure to kill
// is reported.
func (f *FasterWhisper) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cmd == nil {
		return nil
	}
	cmd := f.cmd
	f.cmd = nil
	f.wk = nil
	if err := cmd.Process.Kill(); err != nil {
		// Still reap the process; it may already have exited on its own.
		_ = cmd.Wait()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("asr: 结束 worker 进程失败: %w", err)
	}
	_ = cmd.Wait()
	return nil
}
