package bili

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	referer   = "https://www.bilibili.com"
)

// PlayerV2URL is the unsigned player endpoint used when a video's aid is
// unknown; AISubtitleStatURL resolves the real subtitle_url for AI-only tracks
// whose list entry has an empty subtitle_url.
const (
	PlayerV2URL       = "https://api.bilibili.com/x/player/v2"
	AISubtitleStatURL = "https://api.bilibili.com/x/player/v2/ai/subtitle/search/stat"
)

type Limiter interface {
	Wait(ctx context.Context) error
}

type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bilibili code=%d message=%s", e.Code, e.Message)
}

func (e *APIError) Unauthorized() bool { return e.Code == -101 || e.Code == -400 }

type Client struct {
	HTTP     *http.Client
	SESSDATA string
	Limiter  Limiter
}

func NewClient(sessdata string, limiter Limiter) *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: 20 * time.Second},
		SESSDATA: sessdata,
		Limiter:  limiter,
	}
}

func (c *Client) Get(ctx context.Context, rawURL string, params map[string]string, out any) error {
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return err
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", referer)
	if c.SESSDATA != "" {
		req.Header.Set("Cookie", "SESSDATA="+c.SESSDATA)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if env.Code != 0 {
		return &APIError{Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}
