package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// projectBase is the directory repo-relative files are resolved against. A
// moved project is often started from somewhere else (a shortcut, another
// drive, a service manager with a different working directory), and the
// worker script and .env must still be found next to the binary.
//
// The working directory wins when it looks like the repo, so `go run` (whose
// executable lives in a temporary build directory) keeps working. Otherwise
// the executable's own directory is used.
func projectBase() string {
	if _, err := os.Stat(filepath.Join("scripts", "transcribe_worker.py")); err == nil {
		return "."
	}
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// resolveEnvPath keeps the default .env usable when the process is started from
// another directory. An explicitly passed --env is never rewritten: the user
// named a file and a missing one should be reported, not silently replaced.
func resolveEnvPath(env string, explicit bool) string {
	if explicit || env == "" {
		return env
	}
	if _, err := os.Stat(env); err == nil {
		return env
	}
	return filepath.Join(projectBase(), env)
}

// resolveRepoPath resolves an asset that ships with the repo, such as the ASR
// worker script. It prefers the working directory, then the executable's
// directory.
func resolveRepoPath(rel string) string {
	if _, err := os.Stat(rel); err == nil {
		return rel
	}
	return filepath.Join(projectBase(), rel)
}

// resolvePythonPath turns a relative interpreter path into an absolute one when
// the working directory no longer contains it, so a moved .asr-venv keeps
// working. A bare name ("python") is a PATH lookup and must stay untouched.
func resolvePythonPath(p string) string {
	if p == "" || filepath.IsAbs(p) || !strings.ContainsAny(p, `/\`) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	alt := filepath.Join(projectBase(), p)
	if _, err := os.Stat(alt); err == nil {
		return alt
	}
	return p
}
