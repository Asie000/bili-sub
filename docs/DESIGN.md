# bili-sub 设计说明

> 这份文档讲「为什么这么设计」。怎么装、怎么用，看仓库根目录的 [README](../README.md)。

---

## 一、核心目标

1. **业务目标**
   - 按 **UP主空间（UID）**、**收藏夹（FID）**、**视频合集（Season/Collection）**、**搜索关键词** 批量枚举视频。
   - 优先拉取 B 站自带的官方 / AI 字幕，转成本地字幕文件。
   - 对没有字幕的视频，下载 dash 音频流（`.m4s`，不转码、不落 wav），调用本地 **faster-whisper** 补齐转写。
   - 支撑 **上千个视频** 的离线批处理：断点续传、失败重试、自动去重、风控退避。
   - 多格式导出：页面与 CLI 默认出 **TXT**；`SRT / MD / JSON` 按需选用。

2. **技术学习目标（Java 背景转 Go）**
   - 掌握 **Idiomatic Go**：显式错误处理、组合优于继承、短接口。
   - 吃透 **Goroutine + Channel**、`net/http`、结构体标签反序列化、`os/exec` 子进程、`database/sql`。
   - 体验单二进制分发与高并发 I/O 的开发方式。

---

## 二、总体架构

控制面与算力数据面解耦：

```
                    ┌──────────────────────────────────────────────┐
                    │               Go 核心控制面                   │
                    │                                              │
                    │  1. 登录 / Cookie 管理 (SESSDATA)            │
                    │  2. WBI 签名计算 (Nav 接口 + MD5)             │
                    │  3. 全量分页枚举 (空间 / 收藏夹 / 合集 / 搜索) │
                    │  4. 并发任务调度 (Goroutine Worker Pool)     │
                    │  5. 令牌桶限流 (rate.Limiter，防 412 风控)    │
                    │  6. 多格式渲染 (Canonical IR -> SRT/TXT/MD/JSON)│
                    └───────┬──────────────────────────────┬───────┘
                            │                              │
                   有字幕直接下载落盘              无字幕时下载 dash 音频
                            │                      （data/audio/*.m4s）
                            ▼                              │
                    ┌──────────────┐                       ▼
                    │  本地字幕库   │              ┌───────────────┐
                    │ (.srt / .vtt)│              │ SQLite 任务表 │
                    └──────────────┘              └───────┬───────┘
                                                          │
                                                    轮询 / 任务消费
                                                          │
                                                          ▼
                                                  ┌───────────────┐
                                                  │ ASR 推理引擎  │
                                                  │ (Python Worker│
                                                  │ 或 whisper.cpp)│
                                                  └───────────────┘
```

### 核心解耦原则

- **控制面（Go）**：网络 I/O、协议解析、防封限流、任务去重、文件管理。
- **数据面（Python / C++）**：GPU 显存利用、VAD 切分、批量推理，模型常驻内存。
- **中间格式（Canonical IR）**：转写结果先存通用中间结构，多格式导出纯在内存中转换，**绝不因为换格式重跑模型**。

---

## 三、实现现状（2026-10）

`playurl → dash 音频 → 本地 ASR → srt/IR → 导出` 这条链路已打通，并有真实端到端验证
（首个冒烟测试：223 秒音频 + `tiny` 模型 + CPU int8，25 秒出 84 条字幕）。与最初计划不同的两个决定：

1. **不用 ffmpeg**。faster-whisper 靠 PyAV 解码，`.m4s` 直接喂给 worker 即可，少一次转码、少一次落盘。
2. **CUDA 缺失自动降级**。ctranslate2 只在真正推理时才发现 `cublas64_12.dll` 缺失，worker 会捕获并改用 `cpu/int8`；装了 CUDA 库则自动走 `cuda/float16`。降级只在 GPU **从未成功过**时生效——已经成功过之后再失败，那是文件的问题，不该把整批拖慢 20 倍。

其他已实现的行为：

- **下载和转写重叠**：上一条音频在 GPU 上转写时，下一条已经在下载。worker 仍是一次解码一条，协议没变。
- **转写批处理**：worker 用 `BatchedInferencePipeline`，一次把 8 个 30 秒窗口喂给模型。显存不够可用环境变量 `ASR_BATCH_SIZE` 调小（必须在 shell 环境里设，`.env` 的键不会传给 worker）。
- **输出清洗**（Go 侧，对所有后端生效）：去掉漏出的 `<|0.00|>` 标记、丢乱码/空段、去掉汉字间多余空格、合并连续重复段（静音/音乐处的幻觉）、繁体转简体（`ASR_SIMPLIFY=0` 可关）。
- **字幕语言按语言族匹配**：`--lang zh-Hans` 能选到 `zh-CN`/`zh`/`ai-zh`，但不会选 `zh-Hant`；整批无匹配轨道时整批停下，而不是逐条标失败。
- **输入自动识别**：单视频 / UP主主页 / 收藏夹 / 合集 / 搜索链接，粘哪个都行。
- **先列表、后检查**：「获取视频列表」只翻页拿视频和 cid，不查字幕；「获取字幕」时才逐个检查并标记「无字幕」「充电专属」。
- **Ctrl+C 优雅退出**：不把在途视频标失败，清掉临时音频；第二次 Ctrl+C 强制退出。

---

## 四、技术栈

| 模块 | 技术选型 | 选用理由 |
|---|---|---|
| **核心开发语言** | **Go 1.27**（`go.mod` 要求） | 高并发 I/O、极低内存开销、独立二进制 |
| **网络请求** | Go 标准库 `net/http` | 零第三方重依赖，连接池够用 |
| **并发与限流** | `sync.WaitGroup` + `Channel` + `golang.org/x/time/rate` | 令牌桶平滑限流防 412 |
| **本地状态存储** | **SQLite**（`modernc.org/sqlite` 纯 Go 驱动） | 免 CGO，单文件，轻松支持数万条 |
| **命令行交互** | `spf13/cobra` | 子命令、帮助与参数校验 |
| **音频抽取处理** | **不需要 ffmpeg**：PyAV 自带解码器，直接喂 `.m4s` | 省一次转码 + 一次落盘（一小时 wav 约 115MB） |
| **本地 ASR 引擎** | **faster-whisper**（已实现）；SenseVoice / whisper.cpp 预留未实现 | 复用成熟的 C++/CUDA 内核 |
| **前端看板** | 原生 HTML/CSS/JS（`embed.FS` 内嵌），未上 Vue | 页面只有一张表加几个控件，引入构建链收益不大 |

---

## 五、状态机

数据库里的视频状态：

| 状态 | 含义 |
|---|---|
| `pending` | 刚枚举出来，还没检查字幕 |
| `probed` | 已探测（保留给探测过程中的中间态） |
| `downloadable` | 有可用字幕，等待下载 |
| `no_subtitle` | 没有匹配的字幕，等待转写 |
| `charging_locked` | 充电专属，无法获取字幕 |
| `fetched` | 字幕已下载并落盘 |
| `transcribed` | 本地 ASR 转写完成 |
| `exported` | 已打包进导出 zip |
| `failed` | 失败，可用 `retry` 重置为 `pending` |

`reset` 只清数据目录（`data/`），不碰代码和 `.asr-venv`。

---

## 六、分阶段实战路线

### 阶段一：单文件突破——打通 B 站接口与 Go 基础语法

- [x] **Task 1.1**：用 `net/http` 请求 `https://api.bilibili.com/x/web-interface/view`。
- [x] **Task 1.2**：实现 **WBI 签名**（`/x/web-interface/nav` 拿 key，打乱编码表，MD5 出 `w_rid`）。
- [x] **Task 1.3**：调用 `x/player/wbi/v2` 拿 `subtitle_url`，转成本地 `.srt`。
- 学习点：结构体与 JSON Tag、`if err != nil`、`fmt.Errorf("%w", err)`、切片与指针。

### 阶段二：工程化重构与持久化

- [x] **Task 2.1**：`cmd/`、`internal/bili`、`internal/store`、`internal/config` 目录。
- [x] **Task 2.2**：SQLite 表（`videos`、`transcripts`）。
  - 表名是 `transcripts` 不是 `subtitles`：存的是带时间轴的 canonical IR，导出时再渲染。
- [x] **Task 2.3**：全量分页。空间 `ps=30`、收藏夹 `ps=20`、合集 `page_size=30` 按 `page.total` + 「本页无新视频」双条件收尾，搜索 `page_size=20` 按空页收尾。
- [x] **Task 2.4**：幂等检查——重跑自动跳过已下载的视频。
- 学习点：`go.mod`、包作用域与导出规则、`database/sql` 事务与预编译语句。

### 阶段三：并发池、管道调度与风控限流

- [x] **Task 3.1**：Channel 任务队列 + 固定数量 Worker。
- [x] **Task 3.2**：`golang.org/x/time/rate` 令牌桶（默认 2 req/s）+ 0~200ms 正向抖动（只会拉开，不会变密）。
- [x] **Task 3.3**：412/429 退避重试（2s/5s/12s，3 次）；`-101` 判定登录失效，整条腿停下。
- [x] **Task 3.4**：逐条一行 + 末尾汇总；进度条在网页端（SSE）。
- 学习点：`go func()`、`chan`、`select`、`context.Context`、`sync.WaitGroup`。

### 阶段四：跨语言串联

- [x] **Task 4.1**：`playurl`（`fnval=16` + WBI）取 dash 音频。
- [x] **Task 4.2**：下载到 `data/audio/<bvid>-<cid>.m4s`，直接喂 worker，不经 ffmpeg。
- [x] **Task 4.3**：Python worker，JSON-lines 常驻进程（`scripts/transcribe_worker.py`）。
- [x] **Task 4.4**：转写完成立刻删临时音频。
- 学习点：`os/exec` 子进程、stdin/stdout 管道、临时文件生命周期。

### 阶段五：Web 看板与打包

- [x] **Task 5.1**：Cobra 命令树（`run` / `probe` / `download` / `transcribe` / `export` / `status` / `retry` / `reset` / `serve` / `login`）。
- [x] **Task 5.2**：`net/http` 服务：登录、列表、下载、转写、导出、SSE、任务列表。
  - 默认只监听 `127.0.0.1:8080`：服务**没有任何鉴权**，全网卡可达必须显式选（`--addr :8080`）。
  - 只收 JSON 体（`Content-Type: application/json`），挡掉「随便一个网页往本机 POST」。
- [x] **Task 5.3**：原生页面 + `embed.FS` 单文件打包。

---

## 七、安全底线

- `SESSDATA` / Cookie 只保存在本地 `data/session.json` 与 `.env`，`.gitignore` 已排除，**绝不进仓库**。
- 上千视频必须严控并发与频次（默认 2 req/s、3 并发；`BILI_QPS=8` 属于激进值，容易被 412）。
- 音频生命周期：下载 → 转写落盘 → 校验 → 立即删除，`data/audio/` 不积压。
- Whisper 必须开 VAD，防止静音/背景音产生大段重复幻觉。
- Web 服务无鉴权，默认只绑定本机；需要局域网访问时自行承担风险。
