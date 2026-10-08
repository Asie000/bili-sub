package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	SESSDATA    string
	DataDir     string
	DBPath      string
	Concurrency int
	QPS         float64
	FFmpegPath  string
	PythonPath  string
}

func Default() *Config {
	dataDir := "data"
	return &Config{
		DataDir:     dataDir,
		DBPath:      filepath.Join(dataDir, "bili_sub.db"),
		Concurrency: 3,
		QPS:         2,
		FFmpegPath:  "ffmpeg",
		PythonPath:  "python",
	}
}

// Load 读取 .env 风格文件（KEY=VALUE），缺失则用默认值。
func Load(envPath string) (*Config, error) {
	c := Default()
	if envPath == "" {
		envPath = ".env"
	}
	b, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.applyEnv()
			return c, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		c.set(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
	}
	c.applyEnv()
	return c, nil
}

func (c *Config) applyEnv() {
	for _, k := range []string{"SESSDATA", "BILI_CONCURRENCY", "BILI_QPS", "FFMPEG_PATH", "PYTHON_PATH", "BILI_DATA_DIR"} {
		if v := os.Getenv(k); v != "" {
			c.set(k, v)
		}
	}
}

func (c *Config) set(k, v string) {
	switch k {
	case "SESSDATA":
		c.SESSDATA = v
	case "BILI_DATA_DIR":
		c.DataDir = v
		c.DBPath = filepath.Join(v, "bili_sub.db")
	case "BILI_CONCURRENCY":
		if n, err := strconv.Atoi(v); err == nil {
			c.Concurrency = n
		}
	case "BILI_QPS":
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.QPS = f
		}
	case "FFMPEG_PATH":
		c.FFmpegPath = v
	case "PYTHON_PATH":
		c.PythonPath = v
	}
}
