package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
)

// chromePaths lists where Chrome installs itself, in the order worth trying.
// Windows keeps the 64-bit install under ProgramFiles and the per-user install
// under LOCALAPPDATA; the (x86) entry covers older machines. Empty env vars are
// skipped so a missing ProgramFiles(x86) does not produce a relative path.
func chromePaths() []string {
	switch goruntime.GOOS {
	case "windows":
		var paths []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if dir := os.Getenv(env); dir != "" {
				paths = append(paths, filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
		return paths
	case "darwin":
		return []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	default:
		return nil
	}
}

// chromeNames covers Linux and portable installs, where Chrome is a command on
// PATH rather than a fixed location.
var chromeNames = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}

// findChrome returns the Chrome binary to launch, or "" when none is installed.
// Callers fall back to the OS default browser, so a machine without Chrome still
// opens the page.
func findChrome() string {
	for _, p := range chromePaths() {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	for _, name := range chromeNames {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// openBrowser opens url in Chrome, or in the OS default browser when Chrome is
// not installed.
func openBrowser(url string) error {
	if chrome := findChrome(); chrome != "" {
		return startDetached(chrome, url)
	}
	switch goruntime.GOOS {
	case "windows":
		return startDetached("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		return startDetached("open", url)
	default:
		return startDetached("xdg-open", url)
	}
}

// startDetached starts a process and lets it outlive this one. The handle is
// released instead of waited on: the browser stays open long after serve exits,
// and a Wait would park a goroutine until the user closes it.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
