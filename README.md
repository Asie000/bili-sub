# bili-sub · B 站批量字幕采集与本地 ASR 转写

一个跑在本机的命令行工具 + 网页界面：按 UP主、收藏夹、合集或搜索关键词批量抓 B 站字幕；
没有字幕的视频，用本地 **faster-whisper** 离线转写；最后打包成 TXT / SRT / MD / JSON。

- **不用登录也能列表**，但下载音频和搜索需要登录态（支持扫码 / Cookie）。
- **不需要 ffmpeg**：dash 音频 `.m4s` 直接交给 faster-whisper 解码。
- **不会把视频下载下来**：只取字幕和临时音频，转写完立即删音频。
- 上千个视频可断点续传；Ctrl+C 可优雅退出，不会把在途视频标成失败。

> 设计思路、架构图和 Go 学习路线见 [docs/DESIGN.md](docs/DESIGN.md)。

---

## 目录

- [一、环境要求](#一环境要求)
- [二、部署](#二部署)
- [三、登录 B 站](#三登录-b-站)
- [四、命令行用法](#四命令行用法)
- [五、网页界面](#五网页界面)
- [六、浏览器插件（可选）](#六浏览器插件可选)
- [七、配置项](#七配置项)
- [八、输出文件与状态](#八输出文件与状态)
- [九、常见问题](#九常见问题)
- [十、Python 版](#十python-版)
- [十一、目录结构](#十一目录结构)

---

## 一、环境要求

| 依赖 | 版本 / 说明 |
|---|---|
| 操作系统 | Windows 10/11（主要开发和验证平台）；Linux/macOS 理论上可用，命令里的 `.exe` 改成无后缀即可 |
| Go | **1.27 或更新**（`go.mod` 要求）；只有从源码编译才需要 |
| Python | **3.9–3.12**，仅本地转写需要；3.13+ 通常没有 faster-whisper 预编译包 |
| Node | **20 或更新**，仅修改网页界面时需要；只运行工具不需要 |
| uv 或 pip | 用来建 Python 虚拟环境，任选 |
| 磁盘 | 字幕很小；模型缓存约 0.5–3 GB（按模型大小）；临时音频每条几 MB 到几十 MB，转写完自动删 |
| GPU | 可选。NVIDIA + CUDA 运行库会走 `cuda/float16`，否则自动降级 `cpu/int8` |

---

## 二、部署

### 1. 获取代码并编译

```powershell
cd C:\Users\lsy1\Desktop\BiliBili
go build -o bili-sub.exe ./cmd/bili-sub
```

编译产物是单个 `bili-sub.exe`，网页也内嵌在里面，不需要 Node、不需要再带静态文件。

网页是 Vue 3 + Vite 写的，源码在 `web/`，构建产物已提交到 `internal/server/web/`。
所以只有**改界面**时才需要 Node：

```powershell
cd web
npm install
npm run build     # 产出到 ../internal/server/web/
cd ..
go build -o bili-sub.exe ./cmd/bili-sub
```

改界面时还可以 `npm run dev`（Vite 开发服务器会把 `/api` 转发到 `127.0.0.1:8080`，
一边改一边热更新），但那个模式下要先跑着 `bili-sub serve`。

也可以不编译直接跑：

```powershell
go run ./cmd/bili-sub --help
```

> **必须在仓库根目录运行**（或者用 `start-serve.bat`，它会自动 `cd` 到脚本所在目录）。
> 原因：ASR worker 脚本 `scripts/transcribe_worker.py` 是按当前工作目录找的，换个目录启动会报
> 「找不到 worker 脚本」。

### 2. 建本地 ASR 环境（只用官方字幕可跳过）

**方式 A：uv（推荐）**

```powershell
cd C:\Users\lsy1\Desktop\BiliBili
uv venv --python 3.12 .asr-venv
uv pip install --python .asr-venv\Scripts\python.exe faster-whisper "av<19"
```

**方式 B：系统 Python + venv**

```powershell
cd C:\Users\lsy1\Desktop\BiliBili
py -3.12 -m venv .asr-venv
.\.asr-venv\Scripts\python.exe -m pip install -U pip
.\.asr-venv\Scripts\python.exe -m pip install faster-whisper "av<19"
```

两个注意点：

- **必须 pin `av<19`**。`av>=19` 和 faster-whisper 1.2.x 不兼容，第一次解码就会报
  `open() got an unexpected keyword argument 'metadata_errors'`。
- **venv 不要放进 `data/`**。`bili-sub reset --yes` 会删掉整个数据目录，连 Python 环境一起删。
  `.asr-venv/` 放在仓库根目录即可，`.gitignore` 已经排除。

**GPU（可选）**：

```powershell
uv pip install --python .asr-venv\Scripts\python.exe nvidia-cublas-cu12 nvidia-cudnn-cu12
```

装了这两个包，worker 自动走 `cuda/float16`；没装也不影响，自动降级 `cpu/int8`。

### 3. 配置 `.env`

在仓库根目录复制一份配置模板，再按需修改（`.env` 已被 `.gitignore` 忽略）：

```powershell
Copy-Item .env.example .env
```

`.env` 内容示例：

```dotenv
# 本地 ASR 的 Python 解释器（绝对路径最稳；相对路径按「当前工作目录」解析）
PYTHON_PATH=C:\Users\lsy1\Desktop\BiliBili\.asr-venv\Scripts\python.exe

# 转写模型。留空/注释掉 = worker 默认 small；有 GPU 推荐 large-v3-turbo
ASR_MODEL=large-v3-turbo

# 限流与并发（默认 2 req/s、3 并发，保守值）
BILI_QPS=2
BILI_CONCURRENCY=3
```

> **移动过项目目录？** 现在 `.env` 默认用相对路径 `.asr-venv\Scripts\python.exe`，代码会依次按「当前目录 → `bili-sub.exe` 所在目录」查找，所以只要 venv 跟着项目一起移动，就不用改配置。
> 数据目录仍按当前目录解析，所以还是建议在仓库根目录启动。

验证 Python 环境是否可用：

```powershell
.\.asr-venv\Scripts\python.exe -c "import faster_whisper, av; print('ok')"
```

### 4. 启动

命令行：

```powershell
.\bili-sub.exe --help
```

网页界面（双击 `start-serve.bat` 也可以）：

```powershell
.\bili-sub.exe serve
```

然后打开 <http://127.0.0.1:8080>。

---

## 三、登录 B 站

下载音频（`playurl`）和搜索接口都要求登录态，先登录一次。三种方式任选其一：

```powershell
# 1) 扫码（最省事）
.\bili-sub.exe login --qr

# 2) 整段 Cookie（最稳，能明显降低 412 概率）
#    浏览器 F12 → 网络 → 任意请求 → 请求头 Cookie，整段复制
.\bili-sub.exe login --cookie "SESSDATA=...; bili_jct=...; DedeUserID=..."

# 3) 只给 SESSDATA
.\bili-sub.exe login --sessdata "你的SESSDATA"
```

登录信息保存在 `data/session.json`，之后所有命令自动读取。
`reset --yes` 会连它一起删掉，需要重新登录。

> Cookie 只存在本机，不会上传到任何服务器；也不要把它提交到 Git。

---

## 四、命令行用法

所有命令都支持 `--env <路径>` 指定另一份配置文件（默认 `.env`）。

### `run`：一条命令跑完整条流水线

```powershell
.\bili-sub.exe run "https://www.bilibili.com/video/BV1xx411c7mD"
```

顺序是：枚举 → 探测 → 下载 → 转写（默认开）→ 导出 TXT。
转写失败不会丢掉已下载的字幕，导出照常执行，最后以非零退出码报错。

常用选项：

```powershell
# 只要官方字幕，不转写；导出 srt + txt
.\bili-sub.exe run --transcribe=false --format srt,txt "<URL>"

# 换模型 / 换转写语言
.\bili-sub.exe run --model large-v3-turbo --asr-lang zh "<URL>"

# 统一字幕语言（默认自动选）
.\bili-sub.exe run --lang zh "<URL>"

# 重新处理已经完成的视频
.\bili-sub.exe run --force "<URL>"

# 搜索关键词（默认只取 50 条；0 = 不限）
.\bili-sub.exe run --kind search --limit 50 "劳动法"
```

### 分步执行

| 命令 | 作用 | 常用选项 |
|---|---|---|
| `probe <url>` | 枚举来源并检查字幕情况，**不下载** | `--kind single/space/fav/season/search`、`--force`、`--limit` |
| `download` | 下载可下载的字幕；可选接着转写无字幕视频 | `--lang`、`--format srt,txt,md,json`、`--transcribe`、`--source`、`--asr-lang`、`--model` |
| `transcribe` | 单独跑本地 ASR（只处理 `no_subtitle` 的行） | `--lang`、`--model`、`--source` |
| `export` | 把尚未导出的字幕打包成 zip | `--format txt,srt,md,json`、`--include-exported` |
| `status` | 打印视频状态表 | `--state pending/failed/...` |
| `retry` | 把 `failed` 重置回 `pending` | — |
| `reset` | 清空整个数据目录 | `--yes`（必须） |
| `serve` | 启动网页界面 | `--addr 127.0.0.1:8080` |
| `login` | 保存登录信息 | `--qr` / `--cookie` / `--sessdata` |

典型的手动流程：

```powershell
# 1) 列出视频并检查字幕
.\bili-sub.exe probe "https://space.bilibili.com/123456/video"

# 2) 下载有字幕的；没有的标成 no_subtitle
.\bili-sub.exe download --format srt,txt

# 3) 给没字幕的做本地转写
.\bili-sub.exe transcribe --model large-v3-turbo

# 4) 打包
.\bili-sub.exe export --format txt

# 5) 看看进度
.\bili-sub.exe status
```

只处理某个来源（`--source` 用 `probe` 时解析出的来源键）：

| 来源 | 来源键示例 |
|---|---|
| UP主 | `123456`（mid） |
| 收藏夹 | `7890123`（fid） |
| 合集 | `season:123456:789`（mid:sid） |
| 搜索 | `search:劳动法` |
| 单视频 | `BV1xx411c7mD` |

```powershell
.\bili-sub.exe download --source 123456
.\bili-sub.exe transcribe --source season:123456:789
.\bili-sub.exe download --source search:劳动法
```

### 支持的输入链接

- 单视频：`https://www.bilibili.com/video/BV...`、`av123`、`b23.tv/xxxx`（自动跟随短链）
- UP主空间：`https://space.bilibili.com/<mid>/video`
- 收藏夹：`https://space.bilibili.com/<mid>/favlist?fid=<fid>`
- 合集：`.../channel/collectiondetail?sid=<sid>` 或 `.../lists/<sid>?type=season`
  - 粘单视频链接时，如果它属于某个合集，会提示合集链接，但**不会**自动整批抓。
  - 视频选集（series）暂不支持，会明确报错。
- 搜索：`--kind search "关键词"`（必须登录；一个词最多 1000 条，默认只取 50 条）

> 同一批视频先用 UP主空间抓过、再按合集抓，行的来源键会被改写成合集键；
> 之后用旧的 `--source <mid>` 会查不到（不报错，返回 0 条）。

---

## 五、网页界面

```powershell
.\bili-sub.exe serve
# 或双击 start-serve.bat
```

打开 <http://127.0.0.1:8080>，流程：

1. **登录 / Cookie**：粘贴 SESSDATA 或整段 Cookie，或点「扫码登录」。
2. **来源**：粘贴链接（或选「搜索关键词」填词），点「获取视频列表」。
   - 这一步只翻页拿视频和 cid，不检查字幕，所以很快；列表里先显示「待处理」。
3. 选字幕语言；需要转写就勾「无字幕时转写」，再选转写语言/模型。
4. **获取字幕**：逐个检查，有字幕就下载，没字幕标「无字幕」，充电专属标「充电专属」。
5. **转写无字幕**：当时没勾转写，可以事后单独补跑。
6. **导出**：网页固定导出 TXT；需要别的格式用命令行 `export --format`。
7. 进度通过 SSE 实时显示；「清空」等于命令行 `reset --yes`。

关于监听地址：

- 默认只监听 `127.0.0.1:8080`，别的机器访问不到。
- 这个服务**没有任何鉴权**。确实要用手机/局域网访问时才显式开：
  `.\bili-sub.exe serve --addr :8080`（风险自负）。
- 端口被占用就换一个：`--addr 127.0.0.1:8081`；浏览器插件里也要改成同一端口。

用脚本直接调 API 时，必须带 JSON 请求头，否则服务会拒绝：

```powershell
curl.exe -X POST http://127.0.0.1:8080/api/probe `
  -H "Content-Type: application/json" `
  -d "{\"url\":\"https://www.bilibili.com/video/BV1xx411c7mD\"}"
```

---

## 六、浏览器插件（可选）

`browser-extension/` 里有一个轻量插件：在 B 站页面点一下，就把当前登录 Cookie 和链接发给本机
`bili-sub serve`，省去手动复制。

- 先启动 `.\bili-sub.exe serve`，并保持端口 8080（或在插件弹窗里改）。
- 火狐：`about:debugging#/runtime/this-firefox` → 临时载入 → 选 `browser-extension/manifest.json`。
- Chrome/Edge：先把 `manifest.chrome.json` 复制成 `manifest.json`，再到 `chrome://extensions`
  打开开发者模式 → 加载已解压的扩展程序 → 选择 `browser-extension` 目录。

详细说明见 [browser-extension/README.md](browser-extension/README.md)。

---

## 七、配置项

`.env` 里的键（也可以直接用同名环境变量覆盖 `.env`）：

| 键 | 默认值 | 说明 |
|---|---|---|
| `SESSDATA` | 空 | 登录 Cookie；一般用 `login` 命令写 `data/session.json` 就够了 |
| `BILI_DATA_DIR` | `data` | 数据目录（数据库、字幕、导出、临时音频都在里面） |
| `BILI_QPS` | `2` | 全局请求速率上限（令牌桶）。调高会快，也更容易 412 |
| `BILI_CONCURRENCY` | `3` | 并发任务数 |
| `PYTHON_PATH` | `python` | ASR 用的 Python 解释器；建议指向 `.asr-venv\Scripts\python.exe` |
| `ASR_MODEL` | 空（worker 默认 `small`） | 转写模型；命令行 `--model` 优先级更高 |
| `ASR_SIMPLIFY` | `true` | 繁转简；确实要繁体原文时设 `0` |
| `FFMPEG_PATH` | `ffmpeg` | 预留项，当前链路不需要 ffmpeg |

补充：

- 写了未知键会**直接报错**，不会静默忽略（防止拼错键名后「配置像没生效」）。
- `ASR_BATCH_SIZE`（默认 8）是 worker 的批大小，**必须在 shell 环境里设**，`.env` 里的键不会传给 Python 子进程：
  ```powershell
  $env:ASR_BATCH_SIZE=4
  .\bili-sub.exe transcribe
  ```
- `.env` 默认从当前工作目录读；用 `--env D:\path\to\my.env` 可以换一份。

---

## 八、输出文件与状态

运行后数据目录结构（默认 `data/`）：

```
data/
├── bili_sub.db          # SQLite：视频、字幕 IR、状态
├── session.json         # 登录 Cookie
├── audio/               # 临时 .m4s，转写完立即删（超过 30 分钟的残留会自动清）
├── subtitles/           # 下载/转写的字幕，文件名：UP主 - 标题 [BV号].srt
└── exports/             # 导出的 zip：<日期>-<文件数>.zip，里面按同一命名放各格式
```

- 下载默认落 `.srt`，`--format srt,txt,md,json` 可同时输出多份。
- 导出默认只打包**尚未导出**的字幕；要重复导出加 `--include-exported`。
- 多 P 视频的文件名会带 `_P{n}`，不会互相覆盖。
- 断点续传：已完成的行不会重复下载；`--force`（`probe`/`run`）或 `reset` 才会重来。
- 失败行用 `status --state failed` 查看，`retry` 重置后重跑。

---

## 九、常见问题

### 1. 挪动项目目录后，转写不能用了

几乎都是 `.env` 里的 `PYTHON_PATH` 还指向旧路径。症状：

```
asr: 找不到 Python 解释器 "C:\旧路径\...\python.exe" ...
```

改成新路径（或相对路径 `.asr-venv\Scripts\python.exe`），然后验证：

```powershell
.\.asr-venv\Scripts\python.exe -c "import faster_whisper"
.\bili-sub.exe transcribe --model tiny   # 用最小模型冒烟（需要先有 no_subtitle 的行）
```

其他路径类报错：

| 报错 | 原因 / 处理 |
|---|---|
| `找不到 worker 脚本 scripts/transcribe_worker.py` | 不在仓库根目录启动了；`cd` 回根目录或用 `start-serve.bat` |
| `无法 import faster_whisper` | `PYTHON_PATH` 指到了系统 Python，或没装依赖；用 `.asr-venv` 里的解释器重装 |
| `open() got an unexpected keyword argument 'metadata_errors'` | `av` 版本太新；重装 `faster-whisper "av<19"` |
| `No module named 'ctranslate2'` | 依赖装了一半；在 venv 里重跑安装命令 |

### 2. 转写很慢 / 没用上 GPU

启动时看 stderr：`[worker] large-v3-turbo on cuda/float16` 才是 GPU。

- 显示 `cpu/int8`：没装 CUDA 运行库，装 `nvidia-cublas-cu12 nvidia-cudnn-cu12`。
- 显存爆了：把 `ASR_BATCH_SIZE` 调到 4 或 2（必须在 shell 里设）。
- 降级逻辑只在 GPU **从未成功过**时生效；之前成功过、这次失败，会把它当文件问题，不会偷偷整批降速。

模型选择（RTX 4060 Laptop，同一段 268 秒中文音频）：

| 模型 | GPU float16 | 说明 |
|---|---|---|
| `tiny` | 12 秒 | 质量差，只适合冒烟测试 |
| `small` | 23 秒 | CPU 上的默认选择（int8 约 8 分钟） |
| `large-v3-turbo` | 25 秒 | **有 GPU 就选它**，比 small 只慢 2 秒，准确率明显更高 |

### 3. 被风控（HTTP 412 / 429）

- 把 `BILI_QPS` / `BILI_CONCURRENCY` 调回 2 / 3，等十几分钟再试。
- 用整段 Cookie 重新登录（`login --cookie`），只给 SESSDATA 更容易触发。
- 不要同时开多个终端跑下载/探测。

### 4. 提示登录失效（`-101 账号未登录`）

重新 `login`。下载音频、搜索都必须是登录态。

### 5. 整批停下，提示没有匹配的字幕语言

比如 `--lang ja` 但整批视频只有中文轨道。换成 `--lang zh`（或 `zh-Hans`）重跑；
`zh-Hans` 不会匹配 `zh-Hant`。

### 6. 网页打不开 / 端口冲突

- 确认 `serve` 进程还在，地址是 `http://127.0.0.1:8080`。
- 换端口：`serve --addr 127.0.0.1:8081`；插件弹窗里的端口也要同步改。
- 局域网访问必须显式 `--addr :8080`，并且服务**没有鉴权**。

### 7. 导出是空的

默认只导出「尚未导出」的视频。如果之前打包过，加 `--include-exported`。
另外，只有状态为 `fetched` / `transcribed` / `exported` 且 IR 完好的行才会进包。

### 8. Ctrl+C 之后有残留

- 正常 Ctrl+C 会取消任务、清理在途音频，不标失败；按第二次才强制退出。
- 被强杀留下的旧音频，`transcribe` 每次启动会清掉 30 分钟以上的文件。

---

## 十、Python 版

同仓库的 `python/` 下有一份完整 Python 实现：CLI + Web + 进程内 faster-whisper，和 Go 版共用同一套 `.env`、`data/`、SQLite 表结构和 Web API。

```powershell
cd python
uv venv --python 3.12 .venv
uv pip install --python .venv\Scripts\python.exe -e ".[asr,dev]"
.venv\Scripts\bili-sub-py.exe status
```

安装、命令和差异说明见 [python/README.md](python/README.md)。

> 两个版本不要同时对同一个 `data/` 目录写；切换前先停掉另一个。

---

## 十一、目录结构

```
BiliBili/
├── README.md                  # 本文件：部署与使用
├── docs/DESIGN.md             # 架构、设计取舍、Go 学习路线
├── go.mod / go.sum
├── bili-sub.exe               # 编译产物（gitignore）
├── start-serve.bat            # 双击启动网页界面
├── .env                       # 本地配置（gitignore）
├── .asr-venv/                 # 本地 ASR 的 Python 环境（gitignore）
├── cmd/bili-sub/main.go       # 入口
├── internal/
│   ├── bili/                  # 客户端、WBI 签名、登录、限流
│   ├── store/                 # SQLite 与状态机
│   ├── pipeline/              # 枚举、探测、字幕/音频下载、并发池
│   ├── asr/                   # ASR 调度、worker 协议、输出清洗
│   ├── export/                # IR → srt/txt/md/json + zip 打包
│   ├── server/                # HTTP API、SSE、内嵌网页
│   ├── config/                # .env 解析
│   ├── ir/                    # Canonical IR
│   └── filename/              # 文件名清洗
├── scripts/
│   ├── transcribe_worker.py   # faster-whisper 常驻 worker（Go 版用）
│   └── test_transcribe_worker.py
├── python/                    # Python 完整实现（CLI + Web + 进程内 ASR）
├── web/                       # Vue 3 + Vite 前端源码（改界面才需要 Node）
│   ├── src/lib/               # API 调用、全局状态、SSE 订阅
│   └── src/components/        # 登录、来源、视频表格、进度条、日志
├── browser-extension/         # 浏览器插件（可选）
└── data/                      # 运行产物（gitignore）
```

---

## 说明

- 本项目只抓取你有权限访问的字幕/音频，请在遵守 B 站用户协议与版权规定的前提下使用。
- `.env`、`data/`、`.asr-venv/`、`*.exe`、`web/node_modules/` 都不进 Git；分享代码前不用手动清理。
