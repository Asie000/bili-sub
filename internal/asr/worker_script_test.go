package asr

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestWorkerScriptContract runs the worker script's own Python tests, which stub
// the model: a plain interpreter is enough, no GPU, no faster-whisper and no
// model download. Without an interpreter on PATH the test skips, so the Go suite
// still passes on a machine with no Python -- and when Python is present it pins
// the JSON-lines key names and the "load the model once" promise that the
// in-memory Go fake alone cannot catch.
func TestWorkerScriptContract(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python is not on PATH; skipping the worker script contract test")
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "scripts"))
	if err != nil {
		t.Fatalf("resolve scripts dir: %v", err)
	}
	const script = "test_transcribe_worker.py"
	if _, err := os.Stat(filepath.Join(dir, script)); err != nil {
		t.Fatalf("worker script test not found: %v", err)
	}

	cmd := exec.Command(python, script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worker script contract test failed: %v\n%s", err, out)
	}
}
