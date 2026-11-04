import {
  applyLoginStatus,
  loadLoginStatus,
  loadVideos,
  log,
  markDone,
  showProgress,
  state,
  stopQRStatusPolling,
} from "./store.js";
import { formatEvent } from "./summary.js";

// connectEvents subscribes to the server's SSE stream. EventSource reconnects on
// its own if the connection drops, so there is no retry loop here.
export function connectEvents() {
  const source = new EventSource("/api/events");

  source.onmessage = (e) => {
    let ev;
    try {
      ev = JSON.parse(e.data);
    } catch {
      log(`事件解析失败：${e.data}`);
      return;
    }

    // Progress ticks are high-frequency and never logged: they only move the bar.
    if (ev.type === "progress") {
      const data = ev.data || {};
      showProgress(data.op, data.done, data.total);
      return;
    }

    log(formatEvent(ev));

    switch (ev.type) {
      case "login": {
        const ok = !!(ev.data && ev.data.ok);
        if (ok) {
          state.qr.status = "登录成功，已保存 SESSDATA。";
          applyLoginStatus({ logged_in: true });
          loadLoginStatus();
          log("扫码登录成功。");
        } else if (ev.data && ev.data.error) {
          state.qr.status = `登录失败：${ev.data.error}`;
          stopQRStatusPolling();
          log(`扫码登录失败：${ev.data.error}`);
        }
        break;
      }
      case "handoff": {
        const data = ev.data || {};
        if (data.url) {
          state.form.url = data.url;
        }
        if (data.logged_in) {
          applyLoginStatus({ logged_in: true, uname: data.uname });
        }
        break;
      }
      case "probe":
      case "download":
      case "transcribe":
      case "export":
        // The operation is done: fill the bar and keep it visible with a
        // completion label instead of hiding it.
        markDone(ev.type);
        if (ev.type === "probe" || ev.type === "download") {
          loadVideos();
        }
        break;
      case "reset":
        // Another page cleared the store; reload whatever this page is showing.
        loadVideos();
        break;
    }
  };

  source.onerror = () => {
    // The browser retries automatically; surface it once without spamming.
    if (!connectEvents.warned) {
      connectEvents.warned = true;
      log("SSE 连接中断，浏览器会自动重连。");
    }
  };

  return source;
}
