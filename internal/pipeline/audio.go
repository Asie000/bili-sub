package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/store"
)

// defaultPlayURLBaseURL is the WBI-signed endpoint that hands out the dash
// audio list. Like the player endpoint it needs a signature, so it is used only
// when an Enumerator can supply one.
const defaultPlayURLBaseURL = "https://api.bilibili.com/x/player/wbi/playurl"

// audioHTTPTimeout bounds one audio download. Audio is much larger than a
// subtitle body and a long talk can take minutes on a slow link, so this is
// deliberately generous; Ctrl+C still cancels through the context.
const audioHTTPTimeout = 10 * time.Minute

// AudioFetcher downloads one video's dash audio track to disk, so the ASR
// backend can transcribe it. The three BaseURL/OutputDir/HTTP fields are
// exported so tests can point them elsewhere.
type AudioFetcher struct {
	Client         *bili.Client
	Enumerator     *Enumerator // for the WBI signer (may be nil: no signature, tests only)
	PlayURLBaseURL string
	OutputDir      string
	HTTP           *http.Client
}

func NewAudioFetcher(c *bili.Client, en *Enumerator, outputDir string) *AudioFetcher {
	return &AudioFetcher{
		Client:         c,
		Enumerator:     en,
		PlayURLBaseURL: defaultPlayURLBaseURL,
		OutputDir:      outputDir,
		HTTP:           &http.Client{Timeout: audioHTTPTimeout},
	}
}

// audioTrack is one entry of the playurl response's dash.audio list.
type audioTrack struct {
	BaseURL   string   `json:"baseUrl"`
	BackupURL []string `json:"backupUrl"`
	MimeType  string   `json:"mimeType"`
	Bandwidth int      `json:"bandwidth"`
}

// pickAudioTrack chooses the smallest usable track. Speech survives a low
// bitrate and the file is deleted right after transcription, so downloading the
// 64 kbps track instead of the 192 kbps one is pure time saved. Tracks with no
// URL at all are ignored; among the rest the smallest positive bandwidth wins,
// and a track whose bandwidth the API omitted is only a fallback.
func pickAudioTrack(tracks []audioTrack) (audioTrack, bool) {
	best := -1
	for i, t := range tracks {
		if t.BaseURL == "" && len(t.BackupURL) == 0 {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		smaller := t.Bandwidth > 0 && (tracks[best].Bandwidth == 0 || t.Bandwidth < tracks[best].Bandwidth)
		if smaller {
			best = i
		}
	}
	if best < 0 {
		return audioTrack{}, false
	}
	return tracks[best], true
}

// Fetch resolves the video's dash audio list, downloads the best track and
// returns the local file path. The caller owns that file and should remove it
// as soon as the transcript is safely stored.
func (a *AudioFetcher) Fetch(ctx context.Context, v store.Video) (string, error) {
	if a.Client == nil {
		return "", errors.New("pipeline: AudioFetcher needs a non-nil Client")
	}
	track, err := a.resolve(ctx, v)
	if err != nil {
		return "", err
	}
	return a.download(ctx, v, track)
}

// resolve asks the WBI-signed playurl endpoint for the video's dash audio list
// and picks one track.
func (a *AudioFetcher) resolve(ctx context.Context, v store.Video) (audioTrack, error) {
	params := map[string]string{
		"bvid":  v.Bvid,
		"cid":   strconv.FormatInt(v.Cid, 10),
		"fnval": "16", // 16 = dash, the only format that lists separate audio streams
		"fnver": "0",
		"fourk": "1",
	}
	if a.Enumerator != nil {
		signer, err := a.Enumerator.Signer(ctx)
		if err != nil {
			return audioTrack{}, err
		}
		params, err = signer.Sign(params)
		if err != nil {
			return audioTrack{}, err
		}
	}

	var resp struct {
		Dash struct {
			Audio []audioTrack `json:"audio"`
		} `json:"dash"`
	}
	if err := a.Client.Get(ctx, a.PlayURLBaseURL, params, &resp); err != nil {
		// playurl is one of the endpoints bilibili gates behind a login; an
		// expired session (-101) is the only code worth a login hint. -400 is
		// bilibili rejecting this one request, and telling the user to log in
		// for that would send them chasing the wrong problem.
		var apiErr *bili.APIError
		if errors.As(err, &apiErr) && apiErr.SessionExpired() {
			return audioTrack{}, fmt.Errorf("音频接口要求登录态（%w）：请先运行 `bili-sub login` 再试", err)
		}
		return audioTrack{}, err
	}
	track, ok := pickAudioTrack(resp.Dash.Audio)
	if !ok {
		return audioTrack{}, fmt.Errorf("pipeline: %s 没有可用的音频流（playurl 未返回 dash.audio）", v.Bvid)
	}
	return track, nil
}

// download writes the track to OutputDir, trying the base URL and then each
// backup. A failure leaves no partial file behind: a half-written .m4s would
// otherwise look like a finished download to a later run.
func (a *AudioFetcher) download(ctx context.Context, v store.Video, track audioTrack) (string, error) {
	if err := os.MkdirAll(a.OutputDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(a.OutputDir, fmt.Sprintf("%s-%d.m4s", v.Bvid, v.Cid))

	urls := make([]string, 0, 1+len(track.BackupURL))
	urls = append(urls, track.BaseURL)
	urls = append(urls, track.BackupURL...)

	var lastErr error
	for _, u := range urls {
		if u == "" {
			continue
		}
		if err := a.fetchToFile(ctx, u, path); err == nil {
			return path, nil
		} else {
			lastErr = err
		}
	}
	_ = os.Remove(path)
	if lastErr == nil {
		lastErr = errors.New("no audio URL")
	}
	return "", fmt.Errorf("pipeline: 下载 %s 音频失败: %w", v.Bvid, lastErr)
}

// fetchToFile streams one URL to path. The CDN answers 403 without a bilibili
// referer, so the browser headers go with every attempt; the login cookie is
// deliberately not sent, because the audio URLs are pre-signed and SESSDATA has
// no business leaving the API host.
func (a *AudioFetcher) fetchToFile(ctx context.Context, rawURL, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Referer", browserReferer)

	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: audioHTTPTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
