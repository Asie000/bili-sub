# bili-sub 桥接（浏览器插件）

这个插件把你在 B站（bilibili.com）的登录 Cookie 和当前页面的链接，一次性发送给
**本机**运行的 `bili-sub` 工具，省去手动复制 SESSDATA 的麻烦。

## 安装前准备

1. 先启动本地工具（在项目目录里运行）：`bili-sub serve`
   - 只有它开着，插件发送的 Cookie 才会被接收。
2. 保持工具默认端口 `8080`（若不是 8080，可在插件弹窗里改端口）。

## 火狐 Firefox 安装步骤

1. 先把火狐清单覆盖成 `manifest.json`（在 `browser-extension` 目录里执行）：

   ```powershell
   Copy-Item manifest.firefox.json manifest.json -Force
   ```

2. 地址栏打开 `about:debugging#/runtime/this-firefox`。
3. 点击「临时载入附加组件…」。
4. 选择本目录下的 `manifest.json` 文件。
5. 完成后，工具栏会出现「bili-sub 桥接」图标。

> 说明：这样临时载入的插件在**火狐重启后会失效**，需要重新按上面步骤载入一次。
> 想长期保留必须把插件签名（提交到 addons.mozilla.org）后再安装。
>
> 仓库里的 `manifest.json` 默认是 **Chrome/Edge 版**（`background.service_worker`）。
> 火狐不支持这个键，只认 `background.scripts`：装上去看着没事，但后台脚本不会启动，
> 点图标没反应。所以第 1 步必须换成 `manifest.firefox.json`。

## Chrome / Edge 安装步骤

> 本目录的 `manifest.json` 就是 Chrome/Edge 版本，直接用，不需要改名。
> （如果之前为了火狐把它覆盖过，用 `Copy-Item manifest.chrome.json manifest.json -Force` 换回来。）

1. 地址栏打开 `chrome://extensions`（Edge 为 `edge://extensions`）。
2. 打开右上角的「开发者模式」。
3. 点击「加载已解压的扩展程序」。
4. 选择本目录（`browser-extension` 文件夹本身，不要进到文件里）。
5. 完成后，工具栏会出现「bili-sub 桥接」图标。

## 用法

1. 确认本机已运行 `bili-sub serve`。
2. 在任意 B站 页面点击工具栏上的插件图标。
3. 插件会把 Cookie 和当前链接发送给本机工具；本地页面（`http://localhost:8080`）
   会自动刷新出探测结果。
4. 图标上的小徽标显示结果：`OK`（绿色，发送成功）或 `!`（红色，发送失败）。
   鼠标悬停在图标上可看到探测摘要（可下载／无字幕／失败等）。

> 在非 B站 页面点击也可以，但不会有探测结果；插件会在提示里注明「当前页不是 B 站页面」。

## 隐私

- Cookie 只通过 `http://localhost` 发给你本机的 `bili-sub`，不会上传到任何服务器。
- 插件日志只记录页面 URL 和 Cookie 的**条数**，不记录任何 Cookie 的值。
- 插件只有 `cookies`、`tabs`、`storage` 三个权限，且只对 `*.bilibili.com` 和
  `localhost` 生效，不能读取其它网站的 Cookie。

## 文件说明

| 文件 | 用途 |
| --- | --- |
| `manifest.json` | Chrome/Edge 清单（`background.service_worker`），仓库里默认就是它。 |
| `manifest.chrome.json` | 同一份 Chrome/Edge 清单，用来把覆盖过的 `manifest.json` 换回来。 |
| `manifest.firefox.json` | 火狐清单（`background.scripts`）；用前覆盖 `manifest.json`。 |
| `background.js` | 取当前页 URL 与 Cookie，POST 给本地工具，更新徽标。 |
| `popup.html` / `popup.js` | 图标弹窗：显示最近一次结果与端口设置。 |
