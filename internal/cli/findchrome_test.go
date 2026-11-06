package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantChrome lays down an executable with one of the names findChrome looks for
// on PATH. Windows resolves an executable through PATHEXT, so the .exe twin is
// written there as well, and the extensionless one is what Unix matches.
func plantChrome(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"google-chrome", "google-chrome.exe"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// isolateChrome puts every lookup path out of reach: the Windows install dirs
// point at an empty temporary directory and PATH holds nothing, so whatever
// findChrome returns comes only from what the test planted afterwards.
func isolateChrome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ProgramFiles", dir)
	t.Setenv("ProgramFiles(x86)", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("PATH", t.TempDir())
	return dir
}

// TestFindChromeReturnsEmpty_whenNothingIsInstalled covers the "" return that
// openBrowser depends on: "" is what routes the caller to the OS default
// browser instead of a hard-coded path that does not exist.
func TestFindChromeReturnsEmpty_whenNothingIsInstalled(t *testing.T) {
	isolateChrome(t)

	if got := findChrome(); got != "" {
		t.Fatalf("findChrome() = %q, want empty when no Chrome is installed anywhere", got)
	}
}

// TestFindChromeFallsBackToPath_whenInstallPathIsADirectory replaces the weaker
// "must not equal the directory" assertion: it proves the directory was skipped
// AND that the PATH lookup still found a real binary, so a regression in either
// half fails the test.
func TestFindChromeFallsBackToPath_whenInstallPathIsADirectory(t *testing.T) {
	install := isolateChrome(t)
	asDir := filepath.Join(install, "Google", "Chrome", "Application", "chrome.exe")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	plantChrome(t, binDir)
	t.Setenv("PATH", binDir)

	got := findChrome()
	if got == "" {
		t.Fatal("findChrome() found nothing; the PATH entry planted by the test was not used")
	}
	if got == asDir {
		t.Fatalf("findChrome() = %q; a directory is not a browser binary", got)
	}
	if filepath.Dir(got) != binDir {
		t.Fatalf("findChrome() = %q, want a file inside %q", got, binDir)
	}
	if !strings.HasPrefix(filepath.Base(got), "google-chrome") {
		t.Fatalf("findChrome() = %q, want one of the google-chrome names on PATH", got)
	}
}
