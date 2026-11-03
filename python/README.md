# bili-sub-py

B站字幕批量采集 + 本地 **faster-whisper** 转写，Python 完整实现（CLI + Web）。

这是仓库根目录 Go 版本的等价移植：

- 支持同样的来源：单视频 / UP主空间 / 收藏夹 / 合集 / 搜索
- 同样的四种导出格式：`srt` / `txt` / `md` / `json`
- 同样的 `.env`、`data/` 目录和 SQLite 表结构
- 同样的 Web API 和网页（复用原 `index.html` / `app.js` / `style.css`）
- 转写直接在进程内调用 faster-whisper，没有 Go 版的 Python worker 子进程

> 两个版本不要同时对同一个 `data/` 目录写（SQLite 单文件）。切换版本前先停掉另一个。

---

## 一、安装

需要 **Python 3.10–3.12**（faster-whisper 的预编译包目前只到 3.12）。

```bash
cd python

# 方式 A：uv（推荐）
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python -e ".[asr,dev]"

# 方式 B：系统 Python
py -3.12 -m venv .venv
.venv/Scripts/python -m pip install -U pip
.venv/Scripts/python -m pip install -e ".[asr,dev]"
```

Linux/macOS 把 `.venv/Scripts/python` 换成 `.venv/bin/python`。

只要官方字幕、不本地转写时，可以不带 `asr`：

```bash
pip install -e .
```

---

## 二、配置

默认读取仓库根目录的 `.env`（和 Go 版共用）；在 `python/` 里运行时也会自动向上找。

```dotenv
# 转写模型。留空 = worker 默认 small；有 GPU 推荐 large-v3-turbo
ASR_MODEL=large-v3-turbo

# 限流与并发（默认 2 req/s、3 并发）
BILI_QPS=2
BILI_CONCURRENCY=3

# 数据目录；不写默认用仓库根目录的 data/
# BILI_DATA_DIR=data

# 繁转简（默认开）
# ASR_SIMPLIFY=0
```

说明：

- 所有键和 Go 版一致；`PYTHON_PATH` / `FFMPEG_PATH` 仍然接受（为了 `.env` 兼容），Python 版不使用。
- `.env` 里写了未知键会直接报错。
- `ASR_BATCH_SIZE`（默认 8）是 faster-whisper 的批大小，必须在 shell 环境里设，`.env` 里的同名键不会生效。
- 数据目录默认是 `仓库根目录/data`，所以和 Go 版共用同一份数据库、字幕和登录信息。

---

## 三、登录

```bash
bili-sub-py login --qr
bili-sub-py login --cookie "SESSDATA=...; bili_jct=...; DedeUserID=..."
bili-sub-py login --sessdata "..."
```

登录信息保存在 `data/session.json`（权限 0600）。

---

## 四、命令行

全局参数：

```bash
bili-sub-py --env /path/to/.env <命令>
```

| 命令 | 说明 |
|---|---|
| `probe <url>` | 枚举来源，不下载 |
| `download` | 下载有字幕的视频；可选接着转写 |
| `transcribe` | 只转写 `no_subtitle` 的视频 |
| `export` | 打包尚未导出的字幕 |
| `run <url>` | 一条命令跑完：probe → download → transcribe → export |
| `status` | 视频状态表 |
| `retry` | `failed` 重置为 `pending` |
| `reset --yes` | 清空整个 `data/` |
| `serve` | 启动 Web 界面 |
| `login` | 保存登录信息 |

常用示例：

```bash
# 一条命令跑完（默认转写，导出 txt）
bili-sub-py run "https://www.bilibili.com/video/BV1xx411c7mD"

# 不转写，导出 srt + txt
bili-sub-py run --no-transcribe --format srt,txt "<URL>"

# 手动流程
bili-sub-py probe "https://space.bilibili.com/123456/video"
bili-sub-py download --format srt,txt
bili-sub-py transcribe --model large-v3-turbo
bili-sub-py export --format txt
bili-sub-py status

# 搜索（默认只取 50 条）
bili-sub-py probe --kind search --limit 50 "劳动法"

# 只处理某个来源
bili-sub-py download --source season:123456:789
```

来源键和 Go 版一致：

| 来源 | 键 |
|---|---|
| UP主 | `123456`（mid） |
| 收藏夹 | `7890123`（fid） |
| 合集 | `season:123456:789` |
| 搜索 | `search:劳动法` |
| 单视频 | `BV1xx411c7mD` |

---

## 五、Web 界面

```bash
bili-sub-py serve
# 或
bili-sub-py serve --addr 127.0.0.1:8081
```

打开 <http://127.0.0.1:8080>。

- 与 Go 版共用同一个前端页面和 API（`/api/videos`、`/api/probe`、`/api/download`、`/api/transcribe`、`/api/export`、`/api/events` 等）。
- 默认只监听本机；服务没有鉴权，局域网/公网部署请自行加反向代理和访问控制。
- `serve --addr :8080` 会监听所有网卡。
- API 只接受 `Content-Type: application/json` 的 POST。

---

## 六、输出

```
data/
├── bili_sub.db
├── session.json
├── audio/          # 临时 .m4s，转写完立即删除
├── subtitles/      # UP主 - 标题 [BV号].srt
└── exports/        # YYYYMMDD-N.zip
```

和 Go 版完全相同的命名规则，两个版本可以共用同一个数据目录。

---

## 七、开发与测试

```bash
cd python
.venv/Scripts/python -m pytest
```

测试覆盖：WBI 签名、URL 解析、字幕渲染、文件名清洗、配置、SQLite 状态机、ASR 输出清洗、路径解析。

转写端到端（需要模型缓存 + 一段音频）：

```bash
.venv/Scripts/python -c "
from bili_sub.asr import Transcriber
print(Transcriber('tiny').transcribe('sample.m4s', 'zh')[:2])
"
```

---

## 八、和 Go 版的差异

- 没有 Python worker 子进程和 JSON-lines 协议；`Transcriber` 直接在进程内加载模型，模型只加载一次。
- CUDA 不可用时同样自动降级 `cpu/int8`，并且只在 GPU 从未成功过时降级。
- CLI 布尔参数用 Click 风格：`--no-transcribe`（Go 版是 `--transcribe=false`）。
- 输出文案略有不同，但退出码、状态机和文件产物保持一致。
- Web 的 `/api/login/qrcode` 改用 `segno` 生成二维码 PNG。

---

## 九、注意

- 只抓取你有权限访问的内容；遵守 B站用户协议和版权规定。
- 大量请求会触发 412；`BILI_QPS` / `BILI_CONCURRENCY` 不要开太大。
- 公网部署前先加 HTTPS 和鉴权，当前服务本身没有登录体系。
