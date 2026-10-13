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
| 第二版 | 视频选集（series） | 合集(season)、搜索页 已完成 |
| 第三版 | 播放列表（稍后再看等） | 暫无计划 |

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
                                            │ 下载 dash .m4s     │
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
| 音频 | 无需额外工具：PyAV（faster-whisper 依赖）直接解 `.m4s` | 不转码、不落 wav；`os/exec`+ffmpeg 保留给以后的 whisper.cpp / sherpa |
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
│   ├── bili/                        # client.go wbi.go login.go types.go limiter.go
│   ├── store/                       # db.go schema.go videos.go transcripts.go
│   ├── pipeline/                    # enumerate.go probe.go download.go audio.go pool.go urlparse.go
│   ├── cli/                         # root.go probe.go download.go transcribe.go export.go serve.go …
│   ├── config/                      # config.go（.env / 环境变量）
│   ├── ir/                          # ir.go（Canonical IR）
│   ├── asr/                         # asr.go(接口) fasterwhisper.go worker.go audio.go
│   ├── export/                      # render.go（IR→格式） pack.go（zip）
│   ├── server/                      # http.go sse.go qr.go web.go + web/（内嵌页面）
│   └── util/                        # sanitize.go
├── scripts/
│   ├── transcribe_worker.py         # Python ASR 常驻 worker
│   └── test_transcribe_worker.py    # 它的契约测试（由 go test ./internal/asr 调用）
├── .asr-venv/                       # 本地 ASR 的 Python 环境（gitignore；**刻意放在 data/ 之外**，
│                                    #   因为 `reset --yes` 会删掉整个 data/）
└── data/                            # 运行产物（.gitignore）
    ├── bili_sub.db
    ├── session.json                 # SESSDATA / 完整 Cookie
    ├── audio/                       # 临时音频，转写完立即删
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

- 全局 `rate.Limiter`：默认 2 req/s，每个请求额外等 0~200ms 随机抖动（只拉开间隔，不会变密）。
- Worker 池：3~5 个 goroutine，`chan` 分发任务。
- 412/429：指数退避 + 最大重试次数（默认 3）；超限后该请求报错，按「逐视频失败」处理，不中止整批。
- API `code=-101`：判定 Cookie 失效，暂停并告警（CLI 提示 / Web 顶部横幅）。
- `context.Context` 控制超时与 Ctrl+C 优雅退出：`cmd/bili-sub` 把 `os.Interrupt`（以及非 Windows 上的 `SIGTERM`，即 `docker stop` / systemd / `timeout` 发的那个）接到命令上下文上（第一次取消，第二次恢复默认行为直接退出），所以 `probe`/`download`/`transcribe` 会在中途停下、不把在途视频标成 `failed`、临时音频也能删掉；`serve` 收到取消后先关掉 SSE 订阅再 `Shutdown`（否则那条长连接会让关停白等满宽限期）优雅关停。另加一层保险：`transcribe` 开始前会清掉 `data/audio/` 里 **30 分钟以上**的 `.m4s`——被 OS 硬杀（SIGKILL / 关终端 / 断电）的进程根本没机会跑 defer；加年龄守卫是因为 scratch 目录按 DataDir 共享，而串行化只是进程级的。
- 请求统一带 `User-Agent` + `Referer: https://www.bilibili.com`。
- `SESSDATA` 只从本地配置/.env 读取，绝不入库、绝不进 git。

### 充电视频权限
- 详情/播放接口返回充电字段 → `is_charging=true` 标记。
- 探测阶段判断账号是否满足权限：满足则 `downloadable`；不满足则 `charging_locked`，**跳过不重试**，在清单/UI 明确标注。

---

## 九、ASR 兜底（可选）

> 状态：**已实现并做过真实端到端验证**（2026-10-11）。两个与原计划的偏离见下。

- **运行环境（已定）**：原生 Windows 安装 Python + `faster-whisper`，Go 直接调 `python.exe`，不走 WSL。
  - 需 3.9–3.12（3.13+ 无预编译包）；用 `uv venv --python 3.12 .asr-venv` 建独立环境。
  - 刻意建在 `data/` **之外**：`bili-sub reset --yes` 会 `os.RemoveAll(dataDir)`，把环境建在 `data/` 里会被一并删掉。
  - `av>=19` 与 faster-whisper 1.2.1 不兼容，安装时 pin `"av<19"`。
- **算力（偏离原计划）**：不强制 CUDA。worker 先试 `cuda/float16`，推理时发现 CUDA 库缺失则自动降级 `cpu/int8`（ctranslate2 只在真正推理时才报错，构造时不会）。降级**只在 GPU 从未成功过时**才粘住：GPU 已经成功转录过、之后某个文件失败，说明是这个文件的问题，不该把整批降级。
- **就绪探测**：`asr.Backend.Start()` 在任何视频被碰之前先探一次（worker 脚本是否存在 → `python -c "import faster_whisper"`），所以「没装 Python」「没装 faster-whisper」「脚本路径不对」都是**整条腿级失败**，只报一次并中止，不会变成每个视频各失败一次。
- Python **常驻 worker**：模型只加载一次；Go 与 worker 通过 **stdin/stdout JSON-lines** 通信（`{"audio":"...","lang":"zh|en","model":"small"}` → `{"segments":[...],"error":""}`）。
- **转写语言（已定）**：本地 ASR **只支持中文、英文**两种；命令行 `--lang`（默认 zh，传空则由模型自动检测）。
- **模型选择**：优先级 `--model` > `.env` 的 `ASR_MODEL` > worker 默认（`small`）。默认不设 `ASR_MODEL`，免得新环境一上手就自动下载几个 GB 的模型。
- **输出后处理**：在 Go 侧、在 `asr.Backend` 之外做，所以每个后端都得到同一份可读字幕：去特殊标记、去乱码/空段、去汉字间多余空格、合并连续重复段（Whisper 的幻觉循环）、繁转简。繁简表由 `zhconv` 生成（`internal/asr/trad2simp.go`，4056 字，字级，上下文相关字取最常用简体形）。繁转简可用 `ASR_SIMPLIFY=0` 关掉（台湾等本来就是繁体的内容）。
  - 实测：同一段 268 秒音频，`tiny` 原始输出 175 种繁体字 → 清理后 0；`small` 原始就不带繁体。即繁体主要是小模型的问题。
- 流程（**偏离原计划：去掉 ffmpeg**）：`playurl` 取 dash 音频 m4s → 下载到 `data/audio/` → **直接**喂 worker（PyAV 自带解码器）→ 得 IR 落盘 srt + transcripts → **立即删 m4s**。省掉一次转码和一次 wav 落盘（一小时 wav 约 115MB）。
- **登录要求**：`playurl` 是登录接口，匿名调用返回 `-101 账号未登录`；错误信息会直接提示先 `bili-sub login`。注意只有 `-101` 算「登录态失效」并中止整条腿，`-400` 只是这条请求被拒，仍按逐视频失败处理。
- 后端接口 `asr.Backend`：`Start() error` / `Transcribe(ctx, audioPath, opts) ([]Segment, error)` / `Name() string` / `Close() error`；默认 `faster-whisper`，`sensevoice` 预留。
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

> 状态：基础版已实现（登录 / 探测 / 下载 / 导出 / SSE）；下面标 ⛔ 的部分**尚未实现**，保留为后续计划。

- `bili-sub serve` 启动（默认 `127.0.0.1:8080`，只监听本机）；Go 内嵌 `web/` 下静态文件。服务**无鉴权**，特意默认只听回环——需要局域网/手机访问才用 `--addr :8080` 显式打开。所有写操作端点只接受 `Content-Type: application/json` 的请求体，否则返回 400（这是阻止浏览器里任意页面往本机 POST 的关，代价是 curl 必须带该头）。
- 页面（对齐参考插件）：
  - 顶部：登录状态 / Cookie 获取 / 主题
  - 输入区：粘贴 URL（自动识别类型）+「探测」
  - **语言选择按钮**：探测后列出该视频/当前列表中出现的字幕语言，供选择「拉哪几种语言」；默认按 `中文人工 > 中文AI > 英文 > 其他` 优先取一份，可手动切换。
  - 表格：序号、标题、作者、发布日期、字幕状态（含图标）、字幕语言、是否充电；支持清空、字段设置
  - 操作：一键「获取字幕」「转写无字幕」「导出」；✅ **转写语言选择按钮**（中文 / 英文 / 自动）+ 模型输入框已实现，随请求的 `asr_lang` / `model` 一起发给 `POST /api/download`；留空则回落到 `.env` 的 `ASR_MODEL`
  - 实时进度：**SSE** 推送（探测/下载/转写进度、成功/跳过/失败计数）；✅ 转写腿的 tick 已单独标为 `transcribe`（`legProgress` 带 op），页面进度条会显示「转写 n/N」而不是「获取字幕 n/N」
- 后端 API（JSON）：
  - `POST /api/login/sessdata`、`GET /api/login/qrcode`
  - `POST /api/probe`、`POST /api/download`
  - ⛔ `POST /api/transcribe` 仍未实现（也暂无必要）：ASR 挂在 `POST /api/download` 的 `{"transcribe":true}` 上，可带 `asr_lang` / `model`（搜索的条数上限不在这个接口上，它固定用 `DefaultSearchLimit`）
  - `POST /api/download` 的返回：转写腿失败但已做过事（有计数）时返回 **200 + `error` 字段**，而不是 500——否则页面会丢掉成功计数、看起来像什么都没发生
  - `POST /api/export`
  - `GET /api/videos`、`GET /api/stats`
  - `GET /api/events`（SSE）
- 无 Node 构建；`go run ./cmd/bili-sub serve` 即可开发。

---

## 十二、CLI 命令

| 命令 | 作用 |
|---|---|
| `bili-sub login` | 手填 SESSDATA 或扫码；持久化 |
| `bili-sub run <url>` | 一条命令跑完：枚举 → 探测 → 下载 → 转写 → 导出 |
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

- ASR worker 的 Python 环境：见 §九 —— 原生 Windows Python 3.9–3.12（不走 WSL），venv 建在 `.asr-venv/`。**不要放 `data/` 下**：`reset --yes` 会整目录删除，连 Python 环境一起删。装依赖用 `pip install faster-whisper "av<19"`；模型首次运行自动下载到 HuggingFace 缓存。
- 第二版：合集（已完成）、搜索页（已完成）；视频选集（series）仍未做，现在会明确报「暂不支持」。

### 合集（season）已实现（2026-10-13）

两个入口：

1. **直接粘合集链接**：`space.bilibili.com/<mid>/channel/collectiondetail?sid=<sid>`、
   新的 `space.bilibili.com/<mid>/lists/<sid>?type=season`。来源键为 `season:<mid>:<sid>`；
   枚举走 `x/polymer/web-space/seasons_archives_list`（ps=30 翻页），再逐条 `view` 拿 cid。
2. **粘单视频链接**：若该视频属于某个合集，`view` 的 `ugc_season` 会给出合集名与条数，
   探测时提示「这条视频属于合集《X》，共 N 条」并给出可粘贴的合集链接。
   **不自动展开**：粘 1 条链接就下 31 条太意外，而且请求量差 30 倍；要整合集就粘那个链接。

⚠️ 修复了一个静默错误：合集链接以前会被 `space\.bilibili\.com/(\d+)` 抢先命中，
**不报错**地把 UP主全部视频当成了这个合集（劳动法猫头鹰 54 条 vs 合集 5 条）。
现在合集判定在 space 之前，并且**视频选集（series）**链接会明确报「暂不支持」而不是猜。

> 实测：`合集·劳动消息` 5 条、`合集·100个办公神技` 31 条（>30 真的翻到第 2 页），
> `source_key` 均为 `season:<mid>:<sid>`；series 链接报错。
> 注意：同一批视频若曾以 space 来源抓过，重新按合集抓会**把 source_key 改写成合集键**，
> 按旧键过滤就找不到了。

### 搜索页已实现（2026-10-13）

`bili-sub probe --kind search --limit 20 劳动法`（网页端来源类型选「搜索关键词」）。来源键 `search:<关键词>`（空白会归一化）。

- **必须登录**：匿名的失败点在**取 WBI 密钥那一步**（`nav` 返 `-101`），不是搜索本身被拦。
- **必须用 WBI 那个端点**：`x/web-interface/wbi/search/type`；非 WBI 的 `x/web-interface/search/type` 直接返 **HTML 403**。
- **默认只取 50 条**（`pipeline.DefaultSearchLimit`），`--limit 0` = 不限。一个词最多出 **1000 条**（`numResults` 上限，`numPages` 50），而**每条都要再 `view` 一次拿 cid** → 不限的话就是 1000 请求 ≈ 8 分钟。
- 搜索响应的 **`title` 不用**：它可能带 `<em class="keyword">` 高亮，而且反正每条都要 `view`（拿 cid / 整数时长 / 充电标记），`view` 的标题是干净的。搜索里的 `duration` 是 `"2:12"` 这种**字符串**，也能一并绕开。

> 实测：`--kind search --limit 3 劳动法` → 3 条（新华社/中新视频/川观新闻）、12 秒、`source_key=search:劳动法`。
- 视频列表「字段设置」：第一版固定展示序号/标题/作者/发布日期/字幕状态/字幕语言/是否充电，后续再加可配置。
