package asr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"bilibili-sub/internal/ir"
)

// defaultPython is used when NewFasterWhisper is given an empty pythonPath.
const defaultPython = "python"

// probeTimeout bounds the readiness probe. Importing faster-whisper pulls in
// ctranslate2 and the tokenizers runtime, which takes seconds on a cold cache
// but never minutes; the bound only exists so a wedged interpreter cannot hang
// a whole run.
const probeTimeout = 60 * time.Second

// FasterWhisper runs scripts/transcribe_worker.py as a long-lived child process
// and talks to it over the worker JSON-lines protocol. The process starts on the
// first Start or Transcribe call, so constructing a backend never fails just
// because Python or faster-whisper is absent; only Start/Transcribe report that.
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

// Start launches the worker process ahead of the first file. Transcribe starts
// it lazily too, so this exists for the caller that wants a missing Python, a
// missing faster-whisper or a missing worker script reported once, before any
// video has been touched, rather than as a failure of every video. Starting an
// already-started backend is a no-op.
func (f *FasterWhisper) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startIfNeeded()
}

// startIfNeeded launches the worker unless it is already running. The caller
// must hold f.mu.
func (f *FasterWhisper) startIfNeeded() error {
	if f.wk != nil {
		return nil
	}
	return f.start()
}

// Transcribe starts the worker on first use, then exchanges one request/response
// pair. Calls are serialized: the protocol is a single request/response stream.
func (f *FasterWhisper) Transcribe(ctx context.Context, audioPath string, opts Options) ([]ir.Segment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.startIfNeeded(); err != nil {
		return nil, err
	}
	if opts.Model == "" {
		opts.Model = f.model
	}
	return f.wk.transcribe(ctx, audioPath, opts)
}

// start readies the engine, then launches `pythonPath scriptPath` and wires its
// stdin/stdout into a worker. The child's stderr is inherited so a Python
// traceback reaches the user.
func (f *FasterWhisper) start() error {
	if err := f.probeEngine(); err != nil {
		return err
	}
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

// probeEngine reports whether the worker can actually run, before its process
// is spawned. Spawning alone proves very little: `python worker.py` starts
// perfectly well when faster-whisper is not installed, and the missing engine
// then only surfaces per request -- long after every video's audio has been
// downloaded and every row marked failed. Both failures checked here are
// whole-run problems, so they belong to Start rather than to one video.
func (f *FasterWhisper) probeEngine() error {
	if _, err := os.Stat(f.scriptPath); err != nil {
		return fmt.Errorf("asr: 找不到 worker 脚本 %s（该路径按当前工作目录解析，请在仓库根目录下运行）：%w", f.scriptPath, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	probe := exec.CommandContext(ctx, f.pythonPath, "-c", "import faster_whisper")
	output, err := probe.CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail != "" {
		detail = "：" + detail
	}
	return fmt.Errorf("asr: %s 无法 import faster_whisper（需要 Python 3.9-3.12，并 `pip install faster-whisper \"av<19\"`）：%w%s",
		f.pythonPath, err, detail)
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
