// Package main 是 bilibili-sub 的第一个可运行程序。
//
// 学习目标（对照 Java 习惯）：
//   - Go 用 struct + tag 做 JSON 映射，不需要 Jackson 的注解体系
//   - Go 用「显式返回 error」代替 try/catch 异常
//   - net/http 是标准库，开箱即用，不需要 HttpClient / OkHttp 依赖
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

const (
	apiView   = "https://api.bilibili.com/x/web-interface/view"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	referer   = "https://www.bilibili.com"
)

// biliResp 是 B 站接口的通用外层结构：{code, message, data}
type biliResp struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    *normalInfo `json:"data"`
}

// normalInfo 只声明我们关心的字段，其余字段会被自动忽略。
type normalInfo struct {
	Bvid     string   `json:"bvid"`
	Aid      int64    `json:"aid"`
	Title    string   `json:"title"`
	Duration int      `json:"duration"`
	Owner    owner    `json:"owner"`
	Pages    []page   `json:"pages"`
	Subtitle subtitle `json:"subtitle"`
}

type owner struct {
	Mid  int64  `json:"mid"`
	Name string `json:"name"`
}

type page struct {
	Cid      int64  `json:"cid"`
	Page     int    `json:"page"`
	Part     string `json:"part"`
	Duration int    `json:"duration"`
}

type subtitle struct {
	List []subtitleItem `json:"list"`
}

type subtitleItem struct {
	Lan    string `json:"lan"`
	LanDoc string `json:"lan_doc"`
	URL    string `json:"subtitle_url"`
}

func main() {
	bvid := "BV1uv411q7Mv"
	if len(os.Args) > 1 {
		bvid = os.Args[1]
	}

	info, err := fetchVideoInfo(bvid)
	if err != nil {
		// Go 的错误处理范式：显式检查、就地处理或向上传递。
		fmt.Fprintf(os.Stderr, "获取视频信息失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("标题   : %s\n", info.Title)
	fmt.Printf("BV/AV  : %s / %d\n", info.Bvid, info.Aid)
	fmt.Printf("UP主   : %s (uid=%d)\n", info.Owner.Name, info.Owner.Mid)
	fmt.Printf("总时长 : %d 秒\n", info.Duration)
	fmt.Printf("分P数  : %d\n", len(info.Pages))
	for _, p := range info.Pages {
		fmt.Printf("   - P%d %s (%ds) cid=%d\n", p.Page, p.Part, p.Duration, p.Cid)
	}

	if len(info.Subtitle.List) == 0 {
		fmt.Println("字幕   : 该视频没有字幕（后续可走 ASR 兜底）")
		return
	}
	fmt.Printf("字幕   : 共 %d 条\n", len(info.Subtitle.List))
	for _, s := range info.Subtitle.List {
		fmt.Printf("   - [%s] %s -> %s\n", s.Lan, s.LanDoc, s.URL)
	}
}

// fetchVideoInfo 请求视频详情接口并解析为结构体。
func fetchVideoInfo(bvid string) (*normalInfo, error) {
	req, err := http.NewRequest(http.MethodGet, apiView, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("bvid", bvid)
	req.URL.RawQuery = q.Encode()

	// B 站对缺少 UA / Referer 的请求常返回 412，必须带上。
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", referer)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	// defer 会在函数返回时执行，等价于 Java 的 try-with-resources。
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var r biliResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("bilibili code=%d message=%s", r.Code, r.Message)
	}
	if r.Data == nil {
		return nil, fmt.Errorf("响应缺少 data 字段")
	}
	return r.Data, nil
}
