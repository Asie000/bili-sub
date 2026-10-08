package server

import (
	"encoding/base64"
	"errors"

	qrcode "github.com/skip2/go-qrcode"
)

// qrPNGSize is the rendered QR image size in pixels. Naive UI's default is
// larger, but 320 scans comfortably from a laptop screen and keeps the JSON
// payload around 3 KB.
const qrPNGSize = 320

// qrDataURL renders text as a QR code and returns it as a
// `data:image/png;base64,...` URL, so the embedded page can drop it straight
// into an <img src>. The browser needs no QR library and the tool keeps its
// no-build-step frontend.
func qrDataURL(text string) (string, error) {
	if text == "" {
		return "", errors.New("二维码内容为空")
	}
	png, err := qrcode.Encode(text, qrcode.Medium, qrPNGSize)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
