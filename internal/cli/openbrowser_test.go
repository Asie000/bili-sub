package cli

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

func TestServeURL(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{":8080", "http://localhost:8080"},
		{"localhost:9000", "http://localhost:9000"},
	}
	for _, tc := range tests {
		if got := serveURL(tc.addr); got != tc.want {
			t.Errorf("serveURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// TestFindChromeUsesAnInstalledCopy pins the lookup order: an existing install is
// found even when a higher-priority environment variable points nowhere, which is
// the normal shape on a machine with only one Chrome.
func TestFindChromeUsesAnInstalledCopy(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this test sets the Windows install layout")
	}
	dir := t.TempDir()
	want := filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ProgramFiles", filepath.Join(dir, "no-such-install"))
	t.Setenv("ProgramFiles(x86)", dir)
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "no-such-install-either"))

	if got := findChrome(); got != want {
		t.Errorf("findChrome() = %q, want %q", got, want)
	}
}

// TestFindChromeIgnoresADirectory names the failure mode a bare os.Stat check
// would accept: a directory at the install path is not a binary.
func TestFindChromeIgnoresADirectory(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this test sets the Windows install layout")
	}
	dir := t.TempDir()
	asDir := filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ProgramFiles", dir)
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", "")

	if got := findChrome(); got == asDir {
		t.Error("findChrome() accepted a directory as the browser binary")
	}
}
