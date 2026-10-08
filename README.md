# Bilibili 批量字幕采集与本地 ASR 转写流水线

> **定位**：一个以「实战学习 Go 语言」为核心驱动、面向个人大规模（千级视频）使用的 B 站字幕批量提取与离线 ASR 转写工具。

---

## 一、 项目背景与核心目标

1. **业务目标**：
   - 支持按 **UP主空间（UID）**、**收藏夹（FID）**、**视频合集（Season/Collection）** 批量枚举视频。
   - 优先拉取 B 站自带的官方/AI 字幕（转换保存为 `.srt` 等格式）。
   - 对无字幕视频，自动下载音频流（16kHz 单声道），调用本地 **Whisper / SenseVoice** 模型补齐转写。
   - 稳健支撑 **上千个视频** 的离线批处理，具备断点续传、失败重试、自动去重、风控退避机制。
   - 支持多格式导出（SRT / VTT / ASS / TXT / JSON）。

2. **技术学习目标（Java 背景转向 Go）**：
   - 告别抽象过度与繁重框架，掌握 **Idiomatic Go（地道 Go 语法）**。
   - 深度掌握 Go 的杀手级特性：**Goroutine + Channel 并发调度**、**`net/http` 标准库**、**结构体与标签反序列化**、**`os/exec` 子进程控制**、**`database/sql` 事务**。
   - 体验单二进制独立分发与高性能 I/O 并发的开发心智。

---

## 二、 总体架构设计

流水线采用 **控制面与算力数据面解耦** 的经典架构：

```
                    ┌──────────────────────────────────────────────┐
                    │               Go 核心控制面                   │
                    │                                              │
                    │  1. 登录 / Cookie 管理 (SESSDATA)            │
                    │  2. WBI 签名计算 (Nav 接口 + MD5)             │
                    │  3. 全量分页枚举 (空间 / 收藏夹 / 合集)        │
                    │  4. 并发任务调度 (Goroutine Worker Pool)     │
                    │  5. 令牌桶限流 (rate.Limiter，防 412 风控)    │
                    │  6. 多格式渲染 (Canonical IR -> SRT/VTT/ASS) │
                    └───────┬──────────────────────────────┬───────┘
                            │                              │
                   有字幕直接下载落盘              无字幕时调度 ffmpeg
                            │                      提取音频并写入任务表
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
- **控制面（Go）**：专注网络 I/O、协议解析、防封限流、任务去重、文件管理。
- **数据面（Python / C++）**：专注 GPU 显存利用、VAD 切分、批量推理，模型常驻内存。
- **中间格式（Canonical IR）**：转写结果统一先存通用中间结构，多格式导出纯在内存中转换，**绝不因为换格式重跑模型**。

---

## 三、 技术栈选型

| 模块 | 技术选型 | 选用理由 |
|---|---|---|
| **核心开发语言** | **Go 1.22+** | 高并发 I/O、极低内存开销、编译成独立二进制、学习目标语言 |
| **网络请求** | Go 标准库 `net/http` | 零第三方重依赖，配合连接池已足够强悍 |
| **并发与限流** | `sync.WaitGroup` + `Channel` + `golang.org/x/time/rate` | Go 的并发招牌，令牌桶平滑限流防 412 |
| **本地状态存储** | **SQLite** (`modernc.org/sqlite` 纯 Go 驱动) | 免 CGO 编译，单文件自包含，轻松支持数万条记录 |
| **命令行交互** | `spf13/cobra` 或标准库 `flag` | 构建工业级 CLI 体验（`run`、`status`、`retry`） |
| **音频抽取处理** | 系统安装 **`ffmpeg`**（通过 Go `os/exec` 调用） | 稳定、零内存泄漏提取 16kHz 单声道 wav/flac |
| **本地 ASR 引擎** | **faster-whisper**（N卡）或 **SenseVoice**（中文更优）或 **whisper.cpp**（CPU） | 借助成熟 C++/CUDA 内核，避免语言重复造轮子 |
| **前端看板（可选）** | **Vue 3 + Naive UI + Vite** | 作为后期阶段练习，通过 Go `net/http` 暴露 API / SSE 挂载 |

---

## 四、 分阶段实战路线（学习与开发路线）

### 阶段一：单文件突破——打通 B 站接口与 Go 基础语法
> **目标**：用 1~2 个 `.go` 脚本，跑通单视频的字幕抓取，熟悉 Go 的基本编程习惯。

- [ ] **Task 1.1**：用 `net/http` 请求 `https://api.bilibili.com/x/web-interface/view`，获取视频基本信息。
- [ ] **Task 1.2**：实现 **WBI 签名模块**（调用 `/x/web-interface/nav` 拿 key，打乱编码表，计算 MD5 与 `w_rid` 参数）。
- [ ] **Task 1.3**：调用 `x/player/wbi/v2` 拿 `subtitle_url`，解析并转换为本地 `.srt` 文件（处理时间戳格式 `HH:mm:ss,SSS`）。
- **Go 核心学习点**：
  - 与 Java Bean 对比：结构体（`struct`）与 JSON Tag（`json:"title"`）。
  - 与 Java 异常对比：显式错误处理 `if err != nil`、`fmt.Errorf("%w", err)`。
  - 标准库切片（`slice`）、指针（`pointer`）的基本用法。

---

### 阶段二：工程化重构与持久化——支持批量枚举与断点续传
> **目标**：规范 Go 项目目录结构，引入 SQLite 支持断点续传和全量自动翻页。

- [ ] **Task 2.1**：建立工程目录（`cmd/`、`internal/bili`、`internal/store`、`internal/config`）。
- [ ] **Task 2.2**：设计 SQLite 数据库表（`videos` 表、`subtitles` 表）。
- [ ] **Task 2.3**：实现全量分页循环（空间 `ps=42`、收藏夹 `ps=36`，直到 `has_more=false`），批量写入数据库。
- [ ] **Task 2.4**：实现幂等检查——重跑时自动跳过已经下载完的视频。
- **Go 核心学习点**：
  - `go.mod` 模块依赖管理、包（package）作用域与首字母大小写导出规则。
  - `database/sql` 接口、连接池配置、预编译语句与事务。

---

### 阶段三：Go 的王牌——并发池、管道调度与风控限流
> **目标**：上千级视频的高效抓取，同时保证稳健不封号。

- [ ] **Task 3.1**：构建基于 **Channel** 的任务分发队列，启动固定数量（如 3~5 个）Worker Goroutine。
- [ ] **Task 3.2**：集成 `golang.org/x/time/rate` 令牌桶限流器，限制总 QPS（如 2 req/s）并加入随机抖动（Jitter）。
- [ ] **Task 3.3**：拦截 412/429 响应，实现退避休眠（Backoff）与 Cookie 失效报警。
- [ ] **Task 3.4**：控制台输出友好的进度条与统计（成功/无字幕/失败数）。
- **Go 核心学习点**：
  - `go func()` 协程启动、`chan` 阻塞与非阻塞通信。
  - `select` 多路复用、`context.Context` 优雅超时与退出控制。
  - `sync.WaitGroup` 任务同步等待。

---

### 阶段四：跨语言串联——音频抽取与本地 Whisper 自动转写
> **目标**：当视频检测为「无官方字幕」时，自动走本地模型离线兜底流水线。

- [ ] **Task 4.1**：从 B 站 playurl 接口获取 dash 音频流（m4a）并下载。
- [ ] **Task 4.2**：Go 通过 `os/exec` 调 `ffmpeg` 降采样转换为 `16kHz 单声道 wav/flac`。
- [ ] **Task 4.3**：编写独立的 ASR 常驻脚本/命令（Python `faster-whisper` 或 `whisper.cpp`），Go 负责调度。
- [ ] **Task 4.4**：转写完成后保存 `.srt`，**立刻删除庞大的临时音频文件**，释放磁盘空间。
- **Go 核心学习点**：
  - `os/exec.Command` 子进程生命周期管理、标准输入输出管道捕获。
  - 临时文件安全管理与磁盘 I/O。

---

### 阶段五（加分项）：Web 看板与 CLI 打包
> **目标**：为项目锦上添花，感受 Go 托管静态前端的便利。

- [ ] **Task 5.1**：用 Cobra 封装命令：`bili-sub run --uid 12345`、`bili-sub status`、`bili-sub retry`。
- [ ] **Task 5.2**：Go 标准库启动简易 HTTP 服务，暴露任务列表与统计接口。
- [ ] **Task 5.3**：（可选）Vue 3 编写极简看板，由 Go 的 `embed.FS` 嵌入打包成单个独立 `.exe`。

---

## 五、 重点避坑与安全底线

1. **B 站防爬风控（最容易翻车）**：
   - 抓上千视频必须**严控并发与频次**（推荐 1~3 并发，单请求间隔 1~3 秒）。
   - 请求头带正规 `User-Agent` 与 `Referer: https://www.bilibili.com`。
   - `SESSDATA` 严禁提交至 Git 仓库，统一走本地 `.env` 或配置文件加载。
2. **磁盘空间与生命周期**：
   - 1000 个视频如果全部留存音频，会吃掉上百 GB 空间。
   - 策略必须是：**下载音频 -> 转写成功落盘 srt -> 校验文件存在 -> 立即删除 wav**。
3. **Whisper 算力评估**：
   - 尽量优先匹配 N 卡（CUDA）运行；如果只有 CPU，优先使用 `whisper.cpp` 的量化模型或 `SenseVoiceSmall`。
   - 务必开启 VAD（语音活动检测），防止背景空白产生大段重复幻觉。

---

## 六、 仓库规划目录结构

```
BiliBili/
├── README.md                  # 本实施计划
├── go.mod                     # Go 模块定义
├── go.sum
├── cmd/
│   └── bili-sub/              # 命令行入口 main.go
│       └── main.go
├── internal/
│   ├── bili/                  # B站客户端、WBI签名、API结构体
│   │   ├── client.go
│   │   ├── wbi.go
│   │   └── types.go
│   ├── store/                 # SQLite 任务表读写
│   │   └── db.go
│   ├── downloader/            # 字幕与音频并发下载器
│   │   └── downloader.go
│   ├── asr/                   # ASR 调度 (调用本地模型/脚本)
│   │   └── asr.go
│   └── util/                  # 时间转换、SRT格式拼装等工具
│       └── srt.go
├── scripts/                   # Python 辅助脚本 (faster-whisper worker)
│   └── transcribe.py
└── data/                      # 默认运行产物目录 (被 .gitignore 忽略)
    ├── bili_sub.db            # SQLite 数据库
    └── subtitles/             # 输出字幕目录
```
