package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePythonPathMakesRelativePathsAbsolute covers the moved-project case:
// .env says ".asr-venv\Scripts\python.exe", the venv moved with the repo, and
// the interpreter must resolve no matter what the working directory was.
func TestResolvePythonPathMakesRelativePathsAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rel := filepath.Join(".asr-venv", "Scripts", "python.exe")
	if err := os.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rel, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := resolvePythonPath(rel)
	if !filepath.IsAbs(got) {
		t.Fatalf("resolvePythonPath(%q) = %q, want an absolute path", rel, got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("resolved path %q is not usable: %v", got, err)
	}
}

// TestResolvePythonPathKeepsBareAndMissingNames pins the two values that must
// not be rewritten: a bare name is a PATH lookup, and an unknown relative path
// must keep its spelling so the error names what the user configured.
func TestResolvePythonPathKeepsBareAndMissingNames(t *testing.T) {
	for _, given := range []string{"python", "python3", "definitely/missing/python"} {
		if got := resolvePythonPath(given); got != given {
			t.Errorf("resolvePythonPath(%q) = %q, want it untouched", given, got)
		}
	}
}

// TestResolveEnvPathHonorsExplicitFlag: an explicit --env is the user naming a
// file, so a missing one must be reported rather than silently replaced.
func TestResolveEnvPathHonorsExplicitFlag(t *testing.T) {
	const missing = "no-such-config.env"
	if got := resolveEnvPath(missing, true); got != missing {
		t.Errorf("resolveEnvPath(%q, true) = %q, want it untouched", missing, got)
	}
}

func TestResolveEnvPathPrefersWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(".env", []byte("BILI_QPS=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveEnvPath(".env", false); got != ".env" {
		t.Errorf("resolveEnvPath(.env, false) = %q, want .env", got)
	}
}

func TestResolveRepoPathPrefersWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("scripts", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("scripts", "transcribe_worker.py"), []byte("#"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("scripts", "transcribe_worker.py")
	if got := resolveRepoPath(rel); got != rel {
		t.Errorf("resolveRepoPath(%q) = %q, want the working-directory copy", rel, got)
	}
}
