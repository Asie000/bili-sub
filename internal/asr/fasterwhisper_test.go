package asr

import (
	"io"
	"os"
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
// interpreter cannot run fails at Start, without spawning a worker -- and that it
// says so, rather than blaming faster-whisper for an interpreter that is not
// there at all.
func TestFasterWhisperStartReportsMissingPython(t *testing.T) {
	f := NewFasterWhisper("definitely-not-a-python-binary", workerScriptPath(t), "")
	defer f.Close()

	err := f.Start()
	if err == nil {
		t.Fatal("want an error when the interpreter does not exist")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-python-binary") {
		t.Errorf("error = %q, want it to name the interpreter that is missing", err)
	}
	if !strings.Contains(err.Error(), "PYTHON_PATH") {
		t.Errorf("error = %q, want it to say how to point at one", err)
	}
	if f.wk != nil || f.cmd != nil {
		t.Error("a failed probe must not leave a worker process behind")
	}
}

// TestResolvePythonFallsBackToPython3 covers the default interpreter name: plenty
// of Linux and macOS installs ship python3 and no python at all, and without this
// the whole ASR leg fails with "启动 python 失败" while a perfectly good
// interpreter sits next to it on PATH.
func TestResolvePythonFallsBackToPython3(t *testing.T) {
	dir := t.TempDir()
	// Both names, so the fake PATH resolves whichever OS runs this: Windows finds
	// executables through PATHEXT (so the .exe one), POSIX needs the bare name.
	for _, name := range []string{"python3", "python3.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	if got := resolvePython(""); got != "python3" {
		t.Errorf("resolvePython(\"\") = %q, want python3", got)
	}
	// "python" is the name this code would have picked itself, so asking for it
	// explicitly means the same thing.
	if got := resolvePython(defaultPython); got != "python3" {
		t.Errorf("resolvePython(%q) = %q, want python3", defaultPython, got)
	}
}

// TestResolvePythonKeepsEverythingElse: any other value is the user saying which
// interpreter to run. Substituting a different one would hide a typo in .env
// behind a mysteriously different Python, so it is left exactly as given.
func TestResolvePythonKeepsEverythingElse(t *testing.T) {
	for _, given := range []string{
		"/opt/asr-venv/bin/python",
		`C:\\asr-venv\\Scripts\\python.exe`,
		"definitely-not-a-python-binary",
	} {
		if got := resolvePython(given); got != given {
			t.Errorf("resolvePython(%q) = %q, want it untouched", given, got)
		}
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
