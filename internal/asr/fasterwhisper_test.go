package asr

import (
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workerScriptPath resolves the real worker script from the package directory,
// which is where `go test` runs.
func workerScriptPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "transcribe_worker.py"))
	if err != nil {
		t.Fatalf("resolve worker script: %v", err)
	}
	return path
}

// TestFasterWhisperStartIsNoopWhenAlreadyStarted pins the idempotence the
// transcribe pipeline relies on: it calls Start once up front and Transcribe
// starts the worker too, so a second Start must not spawn a second process. The
// worker field is set directly so no process is created.
func TestFasterWhisperStartIsNoopWhenAlreadyStarted(t *testing.T) {
	f := NewFasterWhisper("python", "scripts/transcribe_worker.py", "")
	f.wk = newWorker(strings.NewReader(""), io.Discard)

	if err := f.Start(); err != nil {
		t.Fatalf("Start on an already-started backend = %v, want nil", err)
	}
}

// TestFasterWhisperStartReportsMissingScript pins the first readiness check: the
// script path is resolved from the working directory, so running the binary from
// somewhere else has to fail at Start rather than once per video.
func TestFasterWhisperStartReportsMissingScript(t *testing.T) {
	f := NewFasterWhisper("python", filepath.Join(t.TempDir(), "no-such-worker.py"), "")
	defer f.Close()

	err := f.Start()
	if err == nil {
		t.Fatal("want an error when the worker script does not exist")
	}
	if !strings.Contains(err.Error(), "脚本") {
		t.Errorf("error = %q, want the missing-script message", err)
	}
}

// TestFasterWhisperStartReportsMissingPython pins that a backend whose
// interpreter cannot run fails at Start, without spawning a worker.
func TestFasterWhisperStartReportsMissingPython(t *testing.T) {
	f := NewFasterWhisper("definitely-not-a-python-binary", workerScriptPath(t), "")
	defer f.Close()

	err := f.Start()
	if err == nil {
		t.Fatal("want an error when the interpreter does not exist")
	}
	if !strings.Contains(err.Error(), "faster_whisper") {
		t.Errorf("error = %q, want the engine-probe message", err)
	}
	if f.wk != nil || f.cmd != nil {
		t.Error("a failed probe must not leave a worker process behind")
	}
}

// TestFasterWhisperStartReportsMissingEngine pins the half of the contract that
// spawning alone cannot see: `python worker.py` starts perfectly well without
// faster-whisper installed, and the missing engine would then surface as a
// per-video failure -- after every video's audio had already been downloaded.
// The interpreter on PATH here is a system Python that has no faster-whisper,
// which is exactly the state the README warns about for Python 3.13+.
func TestFasterWhisperStartReportsMissingEngine(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python is not on PATH; cannot exercise the missing-engine path")
	}
	if err := exec.Command(python, "-c", "import faster_whisper").Run(); err == nil {
		t.Skip("the python on PATH has faster-whisper, so it cannot stand in for a half-installed environment")
	}

	f := NewFasterWhisper(python, workerScriptPath(t), "")
	defer f.Close()

	err = f.Start()
	if err == nil {
		t.Fatal("want an error when the interpreter cannot import faster_whisper")
	}
	if !strings.Contains(err.Error(), "faster_whisper") {
		t.Errorf("error = %q, want it to name the missing engine", err)
	}
	if f.wk != nil || f.cmd != nil {
		t.Error("a failed probe must not leave a worker process behind")
	}
}
