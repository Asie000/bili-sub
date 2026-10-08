package bili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultQRGenerateURL = "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	DefaultQRPollURL     = "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"
	DefaultNavURL        = "https://api.bilibili.com/x/web-interface/nav"

	// data.code values reported by the passport qrcode poll endpoint.
	qrPollNotScan = 86101 // 未扫码
	qrPollScanned = 86090 // 已扫码未确认
	qrPollExpired = 86038 // 二维码已失效

	defaultPollInterval = 2 * time.Second
)

// sessionFile is the on-disk shape of the persisted SESSDATA file.
type sessionFile struct {
	SESSDATA string `json:"sessdata"`
}

// SaveSESSDATA writes sessdata to path as JSON, creating parent directories and
// restricting the file to owner-only access.
func SaveSESSDATA(path, sessdata string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	b, err := json.Marshal(sessionFile{SESSDATA: sessdata})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	// WriteFile keeps the mode of an already existing file, so tighten it here.
	return os.Chmod(path, 0o600)
}

// LoadSESSDATA reads a session file written by SaveSESSDATA. A missing file is
// not an error: it returns "" and nil.
func LoadSESSDATA(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var s sessionFile
	if err := json.Unmarshal(b, &s); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return s.SESSDATA, nil
}

// apiEnvelope is the standard bilibili response shape.
type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// QRLoginClient drives the passport QR-code login flow. Unlike Client, it talks
// to the API directly so it can read response cookies.
type QRLoginClient struct {
	HTTP        *http.Client
	GenerateURL string
	PollURL     string

	pollInterval time.Duration
}

func NewQRLoginClient() *QRLoginClient {
	return &QRLoginClient{
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		GenerateURL:  DefaultQRGenerateURL,
		PollURL:      DefaultQRPollURL,
		pollInterval: defaultPollInterval,
	}
}

// Login generates a QR code, hands its content url to onQR (which may be nil),
// then polls until the code is scanned and confirmed. It returns the SESSDATA
// carried by the poll response.
func (q *QRLoginClient) Login(ctx context.Context, onQR func(qrURL string)) (string, error) {
	env, _, err := q.doJSON(ctx, q.GenerateURL, nil)
	if err != nil {
		return "", fmt.Errorf("generate qrcode: %w", err)
	}
	if env.Code != 0 {
		return "", fmt.Errorf("generate qrcode: %w", &APIError{Code: env.Code, Message: env.Message})
	}
	var gen struct {
		URL       string `json:"url"`
		QRCodeKey string `json:"qrcode_key"`
	}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &gen); err != nil {
			return "", fmt.Errorf("generate qrcode: %w", err)
		}
	}
	if gen.QRCodeKey == "" {
		return "", errors.New("generate qrcode: response carried no qrcode_key")
	}
	if onQR != nil {
		onQR(gen.URL)
	}

	interval := q.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		poll, sessdata, err := q.poll(ctx, gen.QRCodeKey)
		if err != nil {
			return "", err
		}
		switch poll.Code {
		case 0:
			if sessdata == "" {
				return "", errors.New("poll qrcode: login succeeded but response carried no SESSDATA")
			}
			return sessdata, nil
		case qrPollNotScan, qrPollScanned:
			// Still waiting for the user; poll again below.
		case qrPollExpired:
			return "", fmt.Errorf("poll qrcode: QR code expired (code %d), please retry", poll.Code)
		default:
			return "", &APIError{Code: poll.Code, Message: poll.Message}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

// qrPoll is the data payload of a poll response.
type qrPoll struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

// poll performs a single poll request and returns its data payload plus the
// SESSDATA advertised by the response headers or by data.url.
func (q *QRLoginClient) poll(ctx context.Context, qrcodeKey string) (qrPoll, string, error) {
	var data qrPoll
	env, cookies, err := q.doJSON(ctx, q.PollURL, map[string]string{"qrcode_key": qrcodeKey})
	if err != nil {
		return data, "", fmt.Errorf("poll qrcode: %w", err)
	}
	if env.Code != 0 {
		return data, "", fmt.Errorf("poll qrcode: %w", &APIError{Code: env.Code, Message: env.Message})
	}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return data, "", fmt.Errorf("poll qrcode: %w", err)
		}
	}
	sessdata := sessdataFromCookies(cookies)
	if sessdata == "" {
		sessdata = sessdataFromURL(data.URL)
	}
	return data, sessdata, nil
}

// doJSON performs a GET with the browser headers bilibili expects and decodes
// the response envelope, keeping the response cookies for the caller.
func (q *QRLoginClient) doJSON(ctx context.Context, rawURL string, params map[string]string) (apiEnvelope, []*http.Cookie, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return apiEnvelope{}, nil, err
	}
	if len(params) > 0 {
		query := u.Query()
		for k, v := range params {
			query.Set(k, v)
		}
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return apiEnvelope{}, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", referer)

	httpClient := q.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return apiEnvelope{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiEnvelope{}, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return apiEnvelope{}, nil, err
	}
	return env, resp.Cookies(), nil
}

func sessdataFromCookies(cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == "SESSDATA" && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// sessdataFromURL pulls SESSDATA out of the login url the poll response carries.
// It may hold a complete url or a bare cookie-like fragment.
func sessdataFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil {
		if v := u.Query().Get("SESSDATA"); v != "" {
			return v
		}
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '&' || r == '?' || r == ';'
	}) {
		if k, v, ok := strings.Cut(part, "="); ok && k == "SESSDATA" && v != "" {
			return v
		}
	}
	return ""
}

// CheckLogin reports whether the client's SESSDATA is accepted by bilibili.
func (c *Client) CheckLogin(ctx context.Context) (bool, error) {
	return c.CheckLoginAt(ctx, DefaultNavURL)
}

// CheckLoginAt is CheckLogin against an explicit nav endpoint, for tests.
func (c *Client) CheckLoginAt(ctx context.Context, navURL string) (bool, error) {
	var data struct {
		IsLogin bool `json:"isLogin"`
	}
	if err := c.Get(ctx, navURL, nil, &data); err != nil {
		return false, err
	}
	return data.IsLogin, nil
}
