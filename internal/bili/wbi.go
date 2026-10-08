package bili

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// mixinKeyEncTab is the fixed B站 WBI permutation applied to imgKey+subKey
// before the mixin key is taken from its head.
var mixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40,
	61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11,
	36, 20, 34, 44, 52,
}

const mixinKeyLen = 32

type WBISigner struct {
	mixinKey string
}

// NewWBISigner derives the WBI mixin key from the img_url/sub_url keys served by
// B站's nav endpoint. Both keys together must supply the 64 characters that
// mixinKeyEncTab indexes into.
func NewWBISigner(imgKey, subKey string) (*WBISigner, error) {
	raw := imgKey + subKey
	if len(raw) < 64 {
		return nil, fmt.Errorf("wbi key too short: %d", len(raw))
	}
	var sb strings.Builder
	for _, i := range mixinKeyEncTab {
		sb.WriteByte(raw[i])
	}
	return &WBISigner{mixinKey: sb.String()[:mixinKeyLen]}, nil
}

// Sign returns a copy of params with wts (current unix seconds if absent) and
// w_rid added. The signature covers the sorted, URL-escaped query string.
func (s *WBISigner) Sign(params map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(params)+2)
	for k, v := range params {
		out[k] = v
	}
	if _, ok := out["wts"]; !ok {
		out["wts"] = strconv.FormatInt(time.Now().Unix(), 10)
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var q strings.Builder
	for i, k := range keys {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(url.QueryEscape(k))
		q.WriteByte('=')
		q.WriteString(url.QueryEscape(out[k]))
	}
	sum := md5.Sum([]byte(q.String() + s.mixinKey))
	out["w_rid"] = hex.EncodeToString(sum[:])
	return out, nil
}
