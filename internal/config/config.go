package config

import (
	"fmt"
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
	// ASRModel is the Whisper model the local ASR leg uses. Empty means "let the
	// worker script pick", so a fresh checkout does not silently download a
	// multi-gigabyte model on its first run.
	ASRModel string
	// ASRSimplify folds traditional Chinese characters to simplified in the ASR
	// output. On by default, because the smaller whisper models render Mandarin
	// in traditional characters often enough to make the transcript look wrong
	// (measured on one 268s clip: tiny emitted 175 distinct traditional forms,
	// small none). Turn it off to keep a genuinely traditional transcript.
	ASRSimplify bool
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
		ASRSimplify: true,
	}
}

// Load 读取 .env 风格文件（KEY=VALUE），缺失则用默认值。
// 数值无法解析时返回错误，不静默回退到默认值。
func Load(envPath string) (*Config, error) {
	c := Default()
	if envPath == "" {
		envPath = ".env"
	}
	b, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := c.applyEnv(); err != nil {
				return nil, err
			}
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
		key := strings.TrimSpace(kv[0])
		if err := c.set(key, strings.TrimSpace(kv[1])); err != nil {
			return nil, fmt.Errorf("config %s: %w", key, err)
		}
	}
	if err := c.applyEnv(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyEnv() error {
	for _, k := range []string{"SESSDATA", "BILI_CONCURRENCY", "BILI_QPS", "FFMPEG_PATH", "PYTHON_PATH", "BILI_DATA_DIR", "ASR_MODEL", "ASR_SIMPLIFY"} {
		if v := os.Getenv(k); v != "" {
			if err := c.set(k, v); err != nil {
				return fmt.Errorf("config %s: %w", k, err)
			}
		}
	}
	return nil
}

func (c *Config) set(k, v string) error {
	switch k {
	case "SESSDATA":
		c.SESSDATA = v
	case "BILI_DATA_DIR":
		c.DataDir = v
		c.DBPath = filepath.Join(v, "bili_sub.db")
	case "BILI_CONCURRENCY":
		n, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		c.Concurrency = n
	case "BILI_QPS":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.QPS = f
	case "FFMPEG_PATH":
		c.FFmpegPath = v
	case "PYTHON_PATH":
		c.PythonPath = v
	case "ASR_MODEL":
		c.ASRModel = v
	case "ASR_SIMPLIFY":
		b, err := strconv.ParseBool(v)
		if err != nil {
			return err
		}
		c.ASRSimplify = b
	default:
		// A misspelled key must not be silently dropped: the value the user
		// thought they set would otherwise never apply and the failure would
		// surface as "my login/database disappeared".
		return fmt.Errorf("未知配置项 %q", k)
	}
	return nil
}
