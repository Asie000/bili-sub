# bili-sub · B 站字幕批量采集与本地转写

一个跑在自己电脑上的小工具：按 UP主、收藏夹、合集或关键词批量拿 B 站字幕，
没有字幕的视频用本地 **faster-whisper** 转写，最后打包成 TXT / SRT / MD / JSON。

命令行和网页界面都能用。不登录也能列视频，但下载字幕、下载音频和搜索需要登录。

---

## 一、能做什么

| 能力 | 说明 |
|---|---|
| 来源 | UP主空间、收藏夹、合集、搜索关键词、单个视频（`b23.tv` 短链会自动跟随） |
| 字幕 | 优先用 B 站官方 / AI 字幕；没有就下载音频本地转写，转完立即删音频，不留视频文件 |
| 导出 | TXT / SRT / MD / JSON，打包成 zip；默认只打包还没导出过的 |
| 大批量 | 上千条可断点续传，失败能重试，Ctrl+C 不会把在途视频标成失败 |
| 省事 | 不需要 ffmpeg；没有显卡也能转写，有 N 卡会自动用上 |

---

## 二、安装

### 1. 编译

需要 Go 1.27+。在项目目录里执行（**所有命令都要在这个目录里跑**）：

```powershell
cd D:\AAA_CodeRepo\BiliBili   # 换成你自己的项目目录
go build -o bili-sub.exe ./cmd/bili-sub
```

编译产物是单个 exe，网页已经内嵌在里面，不需要 Node。

### 2. 本地转写环境（只要官方字幕可以跳过）

需要 Python 3.9–3.12（3.13+ 装不上 faster-whisper）。

```powershell
uv venv --python 3.12 .asr-venv
uv pip install --python .asr-venv\Scripts\python.exe faster-whisper "av<19"
```

- `av` 必须锁 `av<19`，否则解码第一秒就报 `unexpected keyword argument 'metadata_errors'`。
- 有 N 卡想用 GPU，再装：`uv pip install --python .asr-venv\Scripts\python.exe nvidia-cublas-cu12 nvidia-cudnn-cu12`。
- venv 不要放进 `data/`：`reset --yes` 会连它一起删掉。

### 3. 配置

```powershell
Copy-Item .env.example .env
```

`.env` 里通常只用改三行（完整键见[第六节](#六配置项)）：

```dotenv
PYTHON_PATH=.asr-venv\Scripts\python.exe
ASR_MODEL=large-v3-turbo
BILI_QPS=2
```

### 4. 登录

下载音频和搜索都要求登录态。三选一：

```powershell
.\bili-sub.exe login --qr                            # 扫码，最省事
.\bili-sub.exe login --cookie "SESSDATA=...; bili_jct=..."   # 整段 Cookie，最稳
.\bili-sub.exe login --sessdata "..."                # 只给 SESSDATA
```

登录信息存在 `data/session.json`，之后的命令自动读取。

---

## 三、怎么用

### 一条命令跑完

```powershell
.\bili-sub.exe run "https://www.bilibili.com/video/BV1xx411c7mD"
```

顺序是：列视频 → 查字幕 → 下载 → 转写（默认开）→ 导出 TXT。

常用变体：

```powershell
# 只要官方字幕，不要转写；同时导出 srt 和 txt
.\bili-sub.exe run --transcribe=false --format srt,txt "<URL>"

# 转写用更好的模型
.\bili-sub.exe run --model large-v3-turbo "<URL>"

# 按关键词搜索（默认只取 50 条，0 = 不限）
.\bili-sub.exe run --kind search --limit 50 "劳动法"
```

### 分步执行

大批量、想在中途检查结果时，分成几步更稳：

| 命令 | 作用 |
|---|---|
| `probe <url>` | 列出视频并检查字幕情况，不下载 |
| `download` | 下载有字幕的；没字幕的标成「无字幕」 |
| `transcribe` | 给没字幕的做本地转写 |
| `export` | 打包尚未导出的字幕 |
| `status` | 看每个视频现在是什么状态 |
| `retry` | 把失败的重置回待处理 |
| `reset --yes` | 清空整个数据目录 |
| `serve` | 打开网页界面 |
| `login` | 保存登录信息 |

```powershell
.\bili-sub.exe probe "https://space.bilibili.com/123456/video"
.\bili-sub.exe download --format srt,txt
.\bili-sub.exe transcribe --model large-v3-turbo
.\bili-sub.exe export --format txt
.\bili-sub.exe status
```

### 支持粘贴哪些链接

- 单视频：`https://www.bilibili.com/video/BV...`、`av123`、`b23.tv/xxxx`
- UP主空间：`https://space.bilibili.com/<mid>/video`
- 收藏夹：`https://space.bilibili.com/<mid>/favlist?fid=<fid>`
- 合集：`.../channel/collectiondetail?sid=<sid>` 或 `.../lists/<sid>?type=season`
- 搜索：粘关键词并加 `--kind search`

只处理某一批时用 `--source`（来源键在 `probe` 的输出里有）：
`123456`（UP主）、`7890123`（收藏夹）、`season:123456:789`（合集）、`search:劳动法`、`BV1xx411c7mD`。

> 视频选集（series）暂不支持，会直接报错。粘单视频链接如果它属于某个合集，只会提示合集链接，不会自动整批抓。

---

## 四、网页界面

```powershell
.\bili-sub.exe serve        # 或双击 start-serve.bat
```

默认打开 <http://127.0.0.1:8080>，界面里依次做五件事：

1. 登录：贴 SESSDATA 或整段 Cookie，或点「扫码登录」。
2. 贴链接（或选「搜索关键词」填词），点「获取视频列表」——这步只拿列表，很快。
3. 选字幕语言；需要转写就勾「无字幕时转写」，再选语言和模型。
4. 点「获取字幕」下载，没字幕的会标出来。
5. 点「导出」打包 TXT。要别的格式用命令行 `export --format`。

进度是实时推的，不用刷新；「清空」等于命令行 `reset --yes`。

- 默认只监听本机，别的设备访问不到。**服务没有鉴权**，确实要用手机看再加
  `--addr :8080`，风险自负。
- 端口被占用就换：`serve --addr 127.0.0.1:8081`，插件里的端口也要改成一样。

---

## 五、浏览器插件（可选）

`browser-extension/` 里的小插件：在 B 站页面点一下，就把当前登录 Cookie 和链接发给本机的 `bili-sub serve`，省得手动复制。

- 先跑着 `.\bili-sub.exe serve`，端口保持 8080。
- Chrome / Edge：`chrome://extensions` → 开发者模式 → 加载已解压的扩展程序 → 选 `browser-extension` 目录。
- 火狐：先把 `manifest.firefox.json` 复制成 `manifest.json`，再到 `about:debugging#/runtime/this-firefox` 临时载入。

细节见 [browser-extension/README.md](browser-extension/README.md)。

---

## 六、配置项

`.env` 里的键（也可以用同名环境变量覆盖）：

| 键 | 默认 | 说明 |
|---|---|---|
| `SESSDATA` | 空 | 登录 Cookie；一般用 `login` 写 `data/session.json` 就够了 |
| `BILI_DATA_DIR` | `data` | 数据目录：数据库、字幕、导出、临时音频都在这 |
| `BILI_QPS` | `2` | 每秒最多几个请求。调高更快，也更容易被 412 风控 |
| `BILI_CONCURRENCY` | `3` | 同时处理几个视频 |
| `PYTHON_PATH` | `python` | 转写用的 Python；指向 `.asr-venv\Scripts\python.exe` |
| `ASR_MODEL` | 空（默认 `small`） | 转写模型；命令行的 `--model` 优先 |
| `ASR_SIMPLIFY` | `true` | 繁转简；要繁体原文就设 `0` |
| `FFMPEG_PATH` | `ffmpeg` | 预留项，当前用不到 |

- 写了不认识的键会**直接报错**，不会悄悄忽略，免得拼错键名还以为配置生效了。
- `ASR_BATCH_SIZE`（默认 8）必须在 shell 里设，`.env` 里的同名键传不进 Python 子进程：
  `$env:ASR_BATCH_SIZE=4; .\bili-sub.exe transcribe`
- 换一份配置：`--env D:\path\to\my.env`。

---

## 七、输出在哪

```
data/
├── bili_sub.db          # 数据库：视频、字幕、状态
├── session.json         # 登录 Cookie
├── audio/               # 临时音频，转写完立即删（超 30 分钟的残留会自动清）
├── subtitles/           # 字幕文件：UP主 - 标题 [BV号].srt
└── exports/             # 导出的 zip：<日期>-<文件数>.zip
```

- 字幕默认存 `.srt`；`--format srt,txt,md,json` 可以同时输出多份。
- 多 P 视频的文件名带 `_P{n}`，不会互相覆盖。
- 导出默认只打包还没导出过的；重复导出加 `--include-exported`。
- 已完成的视频不会重复下载；想重来用 `--force` 或 `reset`。

---

## 八、卡住了怎么办

| 现象 | 处理 |
|---|---|
| `找不到 worker 脚本` | 不在项目目录里启动的，`cd` 回来或用 `start-serve.bat` |
| `无法 import faster_whisper` | `PYTHON_PATH` 指错了，或依赖没装全，用 `.asr-venv` 里的解释器重装 |
| `unexpected keyword argument 'metadata_errors'` | `av` 太新，重装 `faster-whisper "av<19"` |
| HTTP 412 / 429 | 被风控了：`BILI_QPS=2`、`BILI_CONCURRENCY=3`，等十几分钟，别同时开多个终端 |
| `-101 账号未登录` | 重新 `login`；下载音频和搜索必须要登录态 |
| 整批停下说没有匹配的字幕语言 | 换 `--lang zh`（`zh-Hans` 不会匹配 `zh-Hant`） |
| 导出是空的 | 默认只导出没导出过的，加 `--include-exported` |
| 转写很慢 | 看启动日志有没有 `cuda/float16`；显示 `cpu/int8` 就是没用上显卡 |

---

## 九、其它

- 架构和设计取舍见 [docs/DESIGN.md](docs/DESIGN.md)。
- 改网页界面：源码在 `web/`，改完 `npm run build` 生成到 `internal/server/web/`，再 `go build` 才会生效。
- 同仓库 `python/` 下有一份完整的 Python 实现（CLI + Web），用法见 [python/README.md](python/README.md)；两份共用同一个 `data/`，别同时跑。
- 只抓你有权限访问的字幕和音频，请遵守 B 站用户协议与版权规定。
