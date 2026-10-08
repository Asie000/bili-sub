# bili-sub 桥接（浏览器插件）

这个插件把你在 B站（bilibili.com）的登录 Cookie 和当前页面的链接，一次性发送给
**本机**运行的 `bili-sub` 工具，省去手动复制 SESSDATA 的麻烦。

## 安装前准备

1. 先启动本地工具（在项目目录里运行）：`bili-sub serve`
   - 只有它开着，插件发送的 Cookie 才会被接收。
2. 保持工具默认端口 `8080`（若不是 8080，可在插件弹窗里改端口）。

## 火狐 Firefox 安装步骤

1. 地址栏打开 `about:debugging#/runtime/this-firefox`。
2. 点击「临时载入附加组件…」。
3. 选择本目录下的 `manifest.json` 文件。
4. 完成后，工具栏会出现「bili-sub 桥接」图标。

> 说明：这样临时载入的插件在**火狐重启后会失效**，需要重新按上面步骤载入一次。
> 想长期保留必须把插件签名（提交到 addons.mozilla.org）后再安装。
>
> 如果火狐提示 `background.service_worker` 不受支持、无法载入，请用本目录下的
> `manifest.firefox.json`：把它重命名为 `manifest.json`（覆盖原文件）后再载入。

## Chrome / Edge 安装步骤

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
| `manifest.json` | 主清单（同时声明 Chrome 的 `service_worker` 与 Firefox 的 `scripts`）。 |
| `manifest.firefox.json` | 仅火狐的备用清单（只声明 `scripts`）。 |
| `background.js` | 取当前页 URL 与 Cookie，POST 给本地工具，更新徽标。 |
| `popup.html` / `popup.js` | 图标弹窗：显示最近一次结果与端口设置。 |
