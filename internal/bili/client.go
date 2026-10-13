package bili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

	// DefaultFingerprintURL hands out the anonymous device cookies (buvid3 /
	// buvid4) that bilibili's wbi endpoints expect. Without them those
	// endpoints answer HTTP 412.
	DefaultFingerprintURL = "https://api.bilibili.com/x/frontend/finger/spi"
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

// Unauthorized reports both codes that mean bilibili did not accept the request:
// -101 (not logged in) and -400 (malformed). Prefer SessionExpired when the
// question is specifically "is the stored login dead?"; this broader predicate is
// kept because it is exported API.
func (e *APIError) Unauthorized() bool { return e.Code == -101 || e.Code == -400 }

// SessionExpired reports the code bilibili uses when the stored login is no
// longer usable. It is deliberately narrower than Unauthorized: -400 is
// bilibili rejecting one malformed request, which must stay a failure of that
// request instead of stopping a whole run or telling the user to log in again.
func (e *APIError) SessionExpired() bool { return e.Code == -101 }

type Client struct {
	HTTP     *http.Client
	SESSDATA string
	// Cookie is a full browser Cookie header string. When it is set, it is sent
	// verbatim and the anonymous device fingerprint is not fetched, because the
	// browser cookie already carries buvid3/buvid4.
	Cookie  string
	Limiter Limiter

	// FingerprintURL is the device-cookie endpoint; it is a field so tests can
	// point it at a local server.
	FingerprintURL string

	// mu guards the lazily fetched device cookies below.
	mu         sync.Mutex
	buvidTried bool
	buvid3     string
	buvid4     string
}

// retryBackoffs are the waits before each 412 retry. B站's 412 is usually a
// short-lived rate limit, so we wait it out a few times before giving up. The
// slice is a variable so tests can shrink it.
var retryBackoffs = []time.Duration{2 * time.Second, 5 * time.Second, 12 * time.Second}

func NewClient(sessdata string, limiter Limiter) *Client {
	return &Client{
		HTTP:           &http.Client{Timeout: 20 * time.Second},
		SESSDATA:       sessdata,
		Limiter:        limiter,
		FingerprintURL: DefaultFingerprintURL,
	}
}

func (c *Client) Get(ctx context.Context, rawURL string, params map[string]string, out any) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	// bilibili's wbi endpoints (space listing, player) reject requests that
	// carry no device fingerprint with HTTP 412, so make sure we have one.
	c.ensureBuvid(ctx)

	resp, err := c.do(ctx, u.String())
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		// B站 answers 412 when its risk control rejects the request. It is
		// usually a short-lived rate limit, so wait it out and retry a few
		// times with the exact same cookies; a full browser Cookie clears it
		// more reliably than a bare SESSDATA.
		for _, wait := range retryBackoffs {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			resp, err = c.do(ctx, u.String())
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusPreconditionFailed {
				break
			}
		}
		if resp.StatusCode == http.StatusPreconditionFailed {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return fmt.Errorf("HTTP 412（B站风控限流）：请求过于频繁被临时拦截，请等几分钟再试；若持续出现，可在页面登录框粘贴浏览器完整 Cookie")
		}
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

// do issues one request with the browser headers and whatever cookies the
// client currently holds.
func (c *Client) do(ctx context.Context, rawURL string) (*http.Response, error) {
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", referer)
	if cookie := c.cookieHeader(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return c.HTTP.Do(req)
}

// sleepCtx waits for d or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cookieHeader returns the Cookie header value. A full browser Cookie is sent
// verbatim; otherwise SESSDATA is joined with the anonymous device cookies.
func (c *Client) cookieHeader() string {
	if c.Cookie != "" {
		return c.Cookie
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := make([]string, 0, 3)
	if c.SESSDATA != "" {
		parts = append(parts, "SESSDATA="+c.SESSDATA)
	}
	if c.buvid3 != "" {
		parts = append(parts, "buvid3="+c.buvid3)
	}
	if c.buvid4 != "" {
		parts = append(parts, "buvid4="+c.buvid4)
	}
	return strings.Join(parts, "; ")
}

// ensureBuvid fetches buvid3/buvid4 once per client. It is a no-op when the
// client carries a full browser Cookie: that cookie already has the device
// fingerprint and appending an anonymous one would defeat it. Failures are not
// fatal: the request simply proceeds without a fingerprint, exactly as before.
func (c *Client) ensureBuvid(ctx context.Context) {
	if c.Cookie != "" {
		return
	}
	c.mu.Lock()
	if c.buvidTried {
		c.mu.Unlock()
		return
	}
	c.buvidTried = true
	c.mu.Unlock()

	b3, b4, err := c.fetchBuvid(ctx)
	if err != nil || b3 == "" {
		return
	}
	c.mu.Lock()
	c.buvid3, c.buvid4 = b3, b4
	c.mu.Unlock()
}

func (c *Client) fingerprintURL() string {
	if c.FingerprintURL != "" {
		return c.FingerprintURL
	}
	return DefaultFingerprintURL
}

// fetchBuvid calls the fingerprint endpoint directly (not through Get, so it
// cannot recurse).
func (c *Client) fetchBuvid(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fingerprintURL(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", referer)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			B3 string `json:"b_3"`
			B4 string `json:"b_4"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", "", err
	}
	if env.Code != 0 {
		return "", "", &APIError{Code: env.Code, Message: "fingerprint spi"}
	}
	return env.Data.B3, env.Data.B4, nil
}
