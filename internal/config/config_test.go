package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Concurrency != 3 {
		t.Errorf("Concurrency = %d, want 3", c.Concurrency)
	}
	if c.QPS != 2 {
		t.Errorf("QPS = %v, want 2", c.QPS)
	}
	if c.DataDir != "data" {
		t.Errorf("DataDir = %q, want %q", c.DataDir, "data")
	}
	if want := filepath.Join("data", "bili_sub.db"); c.DBPath != want {
		t.Errorf("DBPath = %q, want %q", c.DBPath, want)
	}
	if c.FFmpegPath != "ffmpeg" {
		t.Errorf("FFmpegPath = %q, want %q", c.FFmpegPath, "ffmpeg")
	}
	if c.PythonPath != "python" {
		t.Errorf("PythonPath = %q, want %q", c.PythonPath, "python")
	}
	if c.SESSDATA != "" {
		t.Errorf("SESSDATA = %q, want empty", c.SESSDATA)
	}
}

// envKeys are every variable Load consults; clearing them keeps each case
// independent of the developer's real environment.
var envKeys = []string{"SESSDATA", "BILI_CONCURRENCY", "BILI_QPS", "FFMPEG_PATH", "PYTHON_PATH", "BILI_DATA_DIR"}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		envFile string
		env     map[string]string
		want    *Config
		wantErr string
	}{
		{
			name: "valid env file",
			envFile: strings.Join([]string{
				"# comment line is ignored",
				"SESSDATA=sess-value",
				"BILI_CONCURRENCY=5",
				"BILI_QPS=1.5",
				"BILI_DATA_DIR=d2",
				"FFMPEG_PATH=C:/tools/ffmpeg.exe",
				"PYTHON_PATH=python3",
			}, "\n"),
			want: &Config{
				SESSDATA:    "sess-value",
				DataDir:     "d2",
				DBPath:      filepath.Join("d2", "bili_sub.db"),
				Concurrency: 5,
				QPS:         1.5,
				FFmpegPath:  "C:/tools/ffmpeg.exe",
				PythonPath:  "python3",
			},
		},
		{
			name:    "env var overrides file value",
			envFile: "SESSDATA=from-file\nBILI_CONCURRENCY=5\nBILI_QPS=1.5\nFFMPEG_PATH=file-ffmpeg\n",
			env: map[string]string{
				"SESSDATA":         "from-env",
				"BILI_CONCURRENCY": "1",
				"BILI_QPS":         "4.5",
			},
			want: &Config{
				SESSDATA:    "from-env",
				DataDir:     "data",
				DBPath:      filepath.Join("data", "bili_sub.db"),
				Concurrency: 1,
				QPS:         4.5,
				FFmpegPath:  "file-ffmpeg",
				PythonPath:  "python",
			},
		},
		{
			name:    "malformed concurrency in file",
			envFile: "BILI_CONCURRENCY=3x\n",
			wantErr: "BILI_CONCURRENCY",
		},
		{
			name:    "malformed qps in env",
			env:     map[string]string{"BILI_QPS": "2 # note"},
			wantErr: "BILI_QPS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range envKeys {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			path := filepath.Join(t.TempDir(), ".env")
			if tt.envFile != "" {
				if err := os.WriteFile(path, []byte(tt.envFile), 0o600); err != nil {
					t.Fatalf("write env file: %v", err)
				}
			}

			got, err := Load(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() error = nil, want error naming %q", tt.wantErr)
				}
				if want := "config " + tt.wantErr + ":"; !strings.HasPrefix(err.Error(), want) {
					t.Fatalf("Load() error = %q, want prefix %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if *got != *tt.want {
				t.Errorf("Load() = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}
