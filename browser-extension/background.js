// bili-sub 桥接：把当前页面的 URL 与 B 站登录 Cookie 发送给本机 bili-sub。
//
// 同一份脚本同时作为 Firefox 的事件页（background.scripts）与 Chrome 的
// service worker（background.service_worker）运行，因此只用两个浏览器都支持的
// WebExtension API，不引入任何打包步骤。

const api = globalThis.browser ?? globalThis.chrome;

const DEFAULT_PORT = 8080;
const BILIBILI_COOKIE_DOMAIN = "bilibili.com";

// callAsync 统一两种调用风格：Firefox 的 browser.* 与现代 Chrome MV3 都返回
// Promise；极旧的 Chrome 只接受回调，因此在没有拿到 Promise 时退回回调写法。
function callAsync(fn, thisArg, ...args) {
  let maybe;
  try {
    maybe = fn.apply(thisArg, args);
  } catch (_) {
    maybe = undefined;
  }
  if (maybe && typeof maybe.then === "function") {
    return maybe;
  }
  return new Promise((resolve, reject) => {
    try {
      fn.call(thisArg, ...args, (value) => {
        const err = api.runtime.lastError;
        if (err) {
          reject(new Error(err.message));
        } else {
          resolve(value);
        }
      });
    } catch (err) {
      reject(err);
    }
  });
}

// endpoint 读取 popup 保存的端口（默认 8080），拼出本地工具地址。
async function endpoint() {
  let port = DEFAULT_PORT;
  try {
    const stored = await callAsync(api.storage.local.get, api.storage.local, { port: DEFAULT_PORT });
    port = Number.parseInt(stored && stored.port, 10) || DEFAULT_PORT;
  } catch (_) {
    // storage 不可用时退回默认端口。
  }
  return `http://localhost:${port}/api/handoff`;
}

// currentTabURL 返回当前窗口活动标签页的 URL，取不到时返回空串。
async function currentTabURL() {
  const tabs = await callAsync(api.tabs.query, api.tabs, { active: true, currentWindow: true });
  const tab = tabs && tabs[0];
  return tab && tab.url ? tab.url : "";
}

// bilibiliCookie 把 bilibili.com 及其子域的所有 Cookie 拼成 Cookie 请求头。
// 只拼接 name=value，不读取其它字段。
async function bilibiliCookie() {
  const cookies = await callAsync(api.cookies.getAll, api.cookies, { domain: BILIBILI_COOKIE_DOMAIN });
  return (cookies || [])
    .filter((cookie) => cookie && cookie.name)
    .map((cookie) => `${cookie.name}=${cookie.value}`)
    .join("; ");
}

// isBilibili 判断 URL 是否属于 *.bilibili.com。
function isBilibili(url) {
  try {
    const host = new URL(url).hostname;
    return host === "bilibili.com" || host.endsWith(".bilibili.com");
  } catch (_) {
    return false;
  }
}

// summarizeProbe 把探测结果压成一句可读文案，写入悬停标题与 popup。
function summarizeProbe(probe) {
  if (!probe) {
    return "未探测";
  }
  return `可下载 ${probe.downloadable}／无字幕 ${probe.no_subtitle}／充电专属 ${probe.charging_locked}／失败 ${probe.failed}`;
}

// setBadge 更新工具栏徽标与悬停标题。部分环境（图标未固定）会失败，忽略即可。
async function setBadge(text, color, title) {
  try {
    await callAsync(api.action.setBadgeText, api.action, { text });
    await callAsync(api.action.setBadgeBackgroundColor, api.action, { color });
    await callAsync(api.action.setTitle, api.action, { title });
  } catch (_) {
    // 徽标只是提示，失败不影响发送结果。
  }
}

// saveLastResult 把最近一次结果写入 storage，供 popup 展示。
async function saveLastResult(result) {
  try {
    await callAsync(api.storage.local.set, api.storage.local, { lastResult: result });
  } catch (_) {
    // 结果展示是次要功能，写入失败不影响主流程。
  }
}

// handoff 是完整流程：取当前页 URL 与 Cookie，POST 给本地工具，更新徽标。
// 日志只输出 URL 与 Cookie 条数，绝不输出任何 Cookie 值。
async function handoff() {
  const url = await currentTabURL();
  const cookie = await bilibiliCookie();
  const count = cookie ? cookie.split("; ").length : 0;
  console.log(`[bili-sub] handoff url=${url} cookies=${count}`);

  try {
    const res = await fetch(await endpoint(), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ cookie, url }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok || data.ok === false) {
      throw new Error(data.error || `HTTP ${res.status}`);
    }
    const notes = [];
    if (!isBilibili(url)) {
      notes.push("当前页不是 B 站页面");
    }
    if (!data.logged_in) {
      notes.push(data.error ? "登录未通过：" + data.error : "未登录");
    }
    const title = `bili-sub：已发送 Cookie 和链接，请在工具页点「探测」${notes.length ? "（" + notes.join("；") + "）" : ""}`;
    await setBadge("OK", "#2e7d32", title);
    const result = { ok: true, title, probe: data.probe || null, error: data.error || "" };
    await saveLastResult(result);
    return result;
  } catch (err) {
    const message = err && err.message ? err.message : String(err);
    const title = `bili-sub：发送失败：${message}`;
    await setBadge("!", "#c62828", title);
    const result = { ok: false, title, probe: null, error: message };
    await saveLastResult(result);
    return result;
  }
}

// 工具栏图标被点击时直接发送。若 manifest 配置了 default_popup，点击会先打开
// popup（见 popup.js），此处作为没有 popup 时的兜底保持可用。
api.action.onClicked.addListener(() => {
  handoff();
});

// popup 打开时请求一次发送，并把结果回传给 popup 展示。
api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg && msg.type === "handoff") {
    handoff().then(sendResponse);
    return true; // 保持通道打开，等待异步结果。
  }
  return false;
});
