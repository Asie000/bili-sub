# bili-sub 字幕采集与本地 ASR 流水线 · 设计规格

- 日期：2026-10-08
- 状态：待评审
- 定位：以「实战学习 Go」为核心驱动、面向个人千级视频使用的 B站字幕批量采集 + 可选本地 ASR 兜底工具。

---

## 一、目标与范围

### 1.1 要解决的问题
把 B站上一个来源（单视频 / UP主空间 / 收藏夹 / 合集 / 选集 / 搜索页）下的视频**批量**：
1. 先探测有没有**可直接下载的字幕**（不落盘、不浪费时间）；
2. 有字幕的批量下载，转成统一格式；
3. 没有字幕的，**可选**走本地 ASR 兜底；
4. 需要时把结果**打包导出为 zip**。

### 1.2 明确不做（YAGNI）
- 不做 ass / ttml / smi / vtt / lrc / csv 导出（第二批已砍）。
- 不做单文件 exe 打包，不做独立 Vue 工程（前端以 Go 内嵌浏览器页面代替）。
- 不做视频画面下载、弹幕下载、评论抓取。
- 不做投币/点赞/收藏等写操作（纯只读采集 + 登录态）。

### 1.3 版本范围
| 版本 | 输入来源 | 其他 |
|---|---|---|
| **第一版（本次实现）** | 单视频、个人主页(UID)、收藏夹(FID) | login / probe / download / export / status / serve；ASR 接口预留 |
| 第二版 | 合集(season)、视频选集 | |
| 第三版 | 搜索页面 | WBI 分页 + 结果去重 |

---

## 二、关键需求决定（已与用户确认）

1. **导入导出分离**：`probe` 只枚举+探测并写清单，**不下载**；确认后再 `download`。目的是先知道「哪些能直接下」，省时间。
2. **登录双模式**：手填 `SESSDATA` 与 **扫码登录** 都实现；Cookie 持久化到本地，失效可重登。
3. **ASR 可开关**：`download --transcribe` 才启用转写；不加时无字幕视频只标记跳过。
4. **充电视频**：能识别、能标记；**在账号满足权限时才拉取**，否则标记跳过，不重试。
5. **导出为 zip**：文件名 `YYYYMMDD-N`（总结时间-总结数量，无作者）；解压后有一层同名文件夹包裹文件。
6. **导出格式可选**：第一版支持 `srt / txt / json / md`；**只有 srt 带时间轴**，其余为纯内容。
7. **运行环境**：原生 Windows（4060 CUDA）。
8. **前端**：Go 内嵌轻量浏览器页面，无构建步骤、不打包 exe。
9. **代码由 AI 实现**，用户后续学习时再逐段讲解。

---

## 三、整体架构

控制面（Go）与算力数据面（本地模型）解耦：

```
                 ┌───────────────────────────────────────────────┐
                 │                 Go 控制面                      │
                 │  login / WBI 签名 / 来源枚举 / 并发调度 /       │
                 │  rate.Limiter 风控 / ASR 调度 / 导出渲染        │
                 │  HTTP API + 内嵌浏览器页面 (serve)             │
                 └──────┬───────────────────────────┬────────────┘
                        │ 有字幕 → 落盘              │ 无字幕且 --transcribe
                        ▼                           ▼
                 ┌──────────────┐          ┌──────────────────┐
                 │ 字幕库 (.srt) │          │ SQLite 任务表     │
                 │ + IR (.json)   │          └────────┬─────────┘
                 └──────────────┘                   │ 轮询消费
                                                     ▼
                                            ┌──────────────────┐
                                            │ ffmpeg 抽 16k 音频 │
                                            │ → Python ASR worker│
                                            │ → 落盘后删音频     │
                                            └──────────────────┘
```

**核心原则**：转写结果先存 **Canonical IR**，所有导出格式纯内存转换，换格式绝不重跑模型；音频**用完即删**。

---

## 四、技术栈

| 模块 | 选型 | 说明 |
|---|---|---|
| 语言 | Go 1.22+（go.mod 现记录为 1.27，以本机实际安装版本为准） | 学习目标语言 |
| HTTP | 标准库 `net/http` | 零重依赖 |
| 并发/限流 | `sync.WaitGroup` + `chan` + `golang.org/x/time/rate` | 令牌桶防 412 |
| 存储 | SQLite，`modernc.org/sqlite`（纯 Go，免 CGO） | 单文件 |
| CLI | `spf13/cobra` | 工业级命令体验 |
| 音频 | `ffmpeg`（`os/exec` 调用） | 16kHz 单声道 |
| ASR | **faster-whisper**（默认，CUDA/float16）+ 预留 SenseVoice | Python 常驻 worker，JSON-lines 协议 |
| 前端 | Go 内嵌 HTML/CSS/JS（原生，无构建） | 浏览器访问 |
| 打包 | 无（开发期不 embed exe） | |

---

## 五、目录结构

```
BiliBili/
├── README.md
├── go.mod / go.sum
├── cmd/bili-sub/main.go            # CLI 入口与命令注册
├── internal/
│   ├── bili/                        # client.go wbi.go login.go types.go
│   ├── store/                       # db.go schema.go（SQLite + 状态机）
│   ├── pipeline/                    # probe.go download.go（编排）
│   ├── asr/                         # asr.go(接口) fasterwhisper.go
│   ├── export/                      # render.go（IR→格式） pack.go（zip）
│   ├── server/                      # http.go sse.go web/（内嵌页面）
│   └── util/                        # srt.go time.go
├── scripts/transcribe_worker.py     # Python ASR 常驻 worker
├── web/                             # 内嵌浏览器页面（index.html + app.js + style.css）
└── data/                            # 运行产物（.gitignore）
    ├── bili_sub.db
    ├── subtitles/
    └── exports/
```

---

## 六、数据模型（SQLite）

### 6.1 表
- `sources`：`id, type(single|space|fav), input_key, title, owner_mid, created_at`
- `videos`：`id, bvid, cid, aid, mid, author, title, pubdate, duration, page_index, part, is_charging, source_id`
  - 唯一键：`bvid + cid`
- `probes`：`video_id, has_subtitle, lan, lan_doc, subtitle_url, can_download, is_charging_locked, probed_at`
- `transcripts`：`video_id, engine, lang, ir_json, created_at`
- `jobs`：`video_id, kind(probe|download|transcribe|export), state, attempts, last_error, updated_at`
- `meta`：`schema_version`

### 6.2 状态机
```
pending → probed
probed  → downloadable        （有字幕且权限允许）
probed  → no_subtitle         （无字幕，待 --transcribe 决定）
probed  → charging_locked     （充电且当前账号无权，直接跳过）
downloadable → fetched
no_subtitle  → transcribed    （启用 ASR 且成功）
fetched/transcribed → exported
任意 → failed（记录 attempts/last_error，可 retry）
```

### 6.3 幂等
重跑时以 `bvid+cid` 判断，已完成的跳过；`probe` 可强制刷新 `--force`。

---

## 七、Canonical IR（中间表示）

```go
type Segment struct {
    StartMs int64  `json:"start_ms"`
    EndMs   int64  `json:"end_ms"`
    Text    string `json:"text"`
    Speaker string `json:"speaker,omitempty"` // 预留
}
type Document struct {
    Bvid string    `json:"bvid"`
    Cid  int64     `json:"cid"`
    Title string   `json:"title"`
    Author string  `json:"author"`
    Lang  string   `json:"lang"`
    Segments []Segment `json:"segments"`
}
```
IR 始终保留时间轴；**渲染 txt/json/md 时丢弃时间轴**，渲染 srt 时输出。

---

## 八、并发与风控

- 全局 `rate.Limiter`：默认 2 req/s，叠加随机抖动 ±（配置可调）。
- Worker 池：3~5 个 goroutine，`chan` 分发任务。
- 412/429：指数退避 + 最大重试次数（默认 3），超限标 `failed`。
- API `code=-101`：判定 Cookie 失效，暂停并告警（CLI 提示 / Web 顶部横幅）。
- `context.Context` 控制超时与 Ctrl+C 优雅退出。
- 请求统一带 `User-Agent` + `Referer: https://www.bilibili.com`。
- `SESSDATA` 只从本地配置/.env 读取，绝不入库、绝不进 git。

### 充电视频权限
- 详情/播放接口返回充电字段 → `is_charging=true` 标记。
- 探测阶段判断账号是否满足权限：满足则 `downloadable`；不满足则 `charging_locked`，**跳过不重试**，在清单/UI 明确标注。

---

## 九、ASR 兜底（可选）

- **运行环境（已定）**：原生 Windows 安装 Python + `faster-whisper`，Go 直接调 `python.exe`，不走 WSL。
- Python **常驻 worker**：模型只加载一次；Go 与 worker 通过 **stdin/stdout JSON-lines** 通信（`{"audio":"...","lang":"zh|en"}` → `{"segments":[...]}`）。
- **转写语言（已定）**：本地 ASR **只支持中文、英文**两种，通过语言选择按钮指定；不提供其他语种。
- 流程：`playurl` 取 dash 音频 m4a → 下载 → `ffmpeg -ar 16000 -ac 1` 转 wav → 喂 worker → 得 IR 落盘 → **立即删 m4a/wav**。
- 后端接口 `asr.Backend`：`Transcribe(ctx, audioPath, opts) ([]Segment, error)`；默认 `faster-whisper`，`sensevoice` 预留。
- 必须开启 VAD，防止空白段幻觉。
- 失败不阻塞其他视频，标 `failed` 可单独 `retry`。

---

## 十、导出

- 命令：`bili-sub export --format srt,txt,json,md --source <id>`
- 渲染规则：
  - `srt`：带时间轴，`HH:mm:ss,SSS`
  - `txt`：纯文本，无时间轴
  - `md`：标题 + 段落，无时间轴（若开启「带时间戳」选项则加，默认关）
  - `json`：Document 分段结构，无时间轴- 打包：生成 `YYYYMMDD-N.zip`，解压后为同名文件夹 `YYYYMMDD-N/`，内含 N 个字幕文件。
  - `N` = 包内文件数；`YYYYMMDD` = 导出当天日期。
  - 例：`20261008-42.zip → 20261008-42/…（42 个文件）`
- **包内文件命名**：`视频标题 [BV号].srt`（例 `【原神】璃月剧情合集 [BV1uv411q7Mv].srt`）。
  - 标题需清洗 Windows 非法字符 `\ / : * ? " < > |` 与控制符，替换为全角或下划线；超长标题截断（保留 BV 号）。
  - 播放列表/分P 视频：追加 `_P{序号}`（仅在多 P 时）。
- 产物默认落 `data/exports/`。

---

## 十一、Web 界面（浏览器页面）

- `bili-sub serve --addr :8080` 启动；Go 内嵌 `web/` 下静态文件。
- 页面（对齐参考插件）：
  - 顶部：登录状态 / Cookie 获取 / 主题
  - 输入区：粘贴 URL（自动识别类型）+「探测」
  - **语言选择按钮**：探测后列出该视频/当前列表中出现的字幕语言，供选择「拉哪几种语言」；默认按 `中文人工 > 中文AI > 英文 > 其他` 优先取一份，可手动切换。
  - 表格：序号、标题、作者、发布日期、字幕状态（含图标）、字幕语言、是否充电；支持清空、字段设置
  - 操作：一键「获取字幕」「转写无字幕」「导出」；**转写语言选择按钮**（中文 / 英文）
  - 实时进度：**SSE** 推送（探测/下载/转写进度、成功/跳过/失败计数）
- 后端 API（JSON）：
  - `POST /api/login/sessdata`、`GET /api/login/qrcode`
  - `POST /api/probe`、`POST /api/download`、`POST /api/transcribe`
  - `POST /api/export`
  - `GET /api/videos`、`GET /api/stats`
  - `GET /api/events`（SSE）
- 无 Node 构建；`go run ./cmd/bili-sub serve` 即可开发。

---

## 十二、CLI 命令

| 命令 | 作用 |
|---|---|
| `bili-sub login` | 手填 SESSDATA 或扫码；持久化 |
| `bili-sub probe <url>` | 枚举来源并探测字幕可用性（不下载） |
| `bili-sub download` | 下载可下字幕；`--transcribe` 启用 ASR |
| `bili-sub transcribe` | 单独对 `no_subtitle` 视频转写 |
| `bili-sub export --format ...` | 导出 zip |
| `bili-sub status` | tabwriter 表格展示 |
| `bili-sub retry` | 重试 failed |
| `bili-sub serve` | 起 Web 界面 |
| `bili-sub reset --yes` | 清空本地数据 |

---

## 十三、安全底线

1. 严控并发 1~3、单请求间隔 1~3s，防 412。
2. 带正规 UA + Referer。
3. `SESSDATA` 不入库、不进 git，走 `.env`/本地配置。
4. 音频：下载 → 转写成功 → 校验 srt 存在 → 立即删音频。
5. 只读采集，不做任何写操作。

---

## 十四、验收标准（第一版）

- [ ] `login` 两种方式都能拿到有效 Cookie 并持久化，重启仍有效。
- [ ] `probe <单视频>` 能输出有无字幕、语言、是否可直接下载、是否充电锁定。
- [ ] `probe <UP主UID>` / `probe <收藏夹FID>` 能全量分页枚举并落库，重跑幂等跳过。
- [ ] `download` 能并发下载字幕并落 `srt`；无字幕且未开 `--transcribe` 时正确标记。
- [ ] 充电视频无权限时标 `charging_locked` 且不重试。
- [ ] `export --format srt,txt,json,md` 产物为 `YYYYMMDD-N.zip`，解压后同名文件夹，只有 srt 带时间轴。
- [ ] `serve` 浏览器可打开，表格显示字段与参考插件一致，SSE 进度实时更新。
- [ ] 1000 个视频级任务不崩、内存稳定、可断点续传。

---

## 十五、后续（非第一版）

- ASR worker 的 Python 环境：沿用已装好的 WSL/Windows Python，用 `uv` 建 venv 并安装 `faster-whisper`；模型缓存路径固定到 `data/models/`，首次运行自动下载。
- 第二版：合集 + 视频选集；第三版：搜索页。
- 视频列表「字段设置」：第一版固定展示序号/标题/作者/发布日期/字幕状态/字幕语言/是否充电，后续再加可配置。
