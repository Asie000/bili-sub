package asr

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// defaultFFmpeg is used when ExtractAudio is given an empty ffmpegPath.
const defaultFFmpeg = "ffmpeg"

// ExtractAudio decodes in into a 16 kHz mono WAV at out, the shape
// faster-whisper expects. ffmpeg's stderr is captured into the returned error
// so a missing binary or an unsupported input is diagnosable without a panic.
func ExtractAudio(ctx context.Context, ffmpegPath, in, out string) error {
	if ffmpegPath == "" {
		ffmpegPath = defaultFFmpeg
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, "-y", "-i", in, "-ar", "16000", "-ac", "1", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("asr: ffmpeg 抽取音频失败（%s）: %w: %s", ffmpegPath, err, msg)
		}
		return fmt.Errorf("asr: ffmpeg 抽取音频失败（%s，未找到可执行文件？请安装 ffmpeg 并加入 PATH）: %w", ffmpegPath, err)
	}
	return nil
}
