import { describeDownload, describeTranscribe } from "./describe.js";

// formatEvent turns one SSE payload into the line the log shows.
export function formatEvent(ev) {
  const data = ev.data || {};
  switch (ev.type) {
    case "probe":
      return `获取事件：已列出 ${data.probed} 条`;
    case "download":
      return `下载事件：${describeDownload(data)}`;
    case "transcribe":
      return `转写事件：${describeTranscribe(data)}`;
    case "export":
      return `导出事件：${data.zip}`;
    case "reset":
      return `数据已清空：删除 ${data.deleted} 条记录`;
    case "login":
      return data.error ? `扫码登录失败：${data.error}` : "扫码登录成功，已保存 SESSDATA。";
    case "handoff": {
      const parts = [];
      if (data.url) {
        parts.push("已填入插件发来的链接，请点「获取视频列表」");
      }
      if (data.logged_in) {
        parts.push(`登录成功${data.uname ? `（${data.uname}）` : ""}`);
      } else if (data.error) {
        parts.push(`登录失败：${data.error}`);
      }
      return parts.length ? `浏览器插件：${parts.join("，")}` : "浏览器插件：已收到";
    }
    default:
      return `事件 ${ev.type}：${JSON.stringify(data)}`;
  }
}

// looksLikeCookie mirrors bili.LooksLikeCookie: a full browser Cookie header
// carries SESSDATA= or more than one ';'-separated key=value pair. The server
// derives SESSDATA from it, so the page only has to pick the right field.
export function looksLikeCookie(value) {
  if (value.includes("SESSDATA=")) {
    return true;
  }
  const pairs = value
    .split(";")
    .map((part) => part.trim())
    .filter((part) => {
      const eq = part.indexOf("=");
      return eq > 0;
    });
  return pairs.length > 1;
}
