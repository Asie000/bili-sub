// popup 打开时触发一次发送（由 background.js 执行），并展示结果与端口设置。

"use strict";

const api = globalThis.browser ?? globalThis.chrome;

const DEFAULT_PORT = 8080;

// sendMessage 兼容 Firefox（Promise）与旧 Chrome（回调）两种风格。
function sendMessage(msg) {
  const maybe = api.runtime.sendMessage(msg);
  if (maybe && typeof maybe.then === "function") {
    return maybe;
  }
  return new Promise((resolve, reject) => {
    api.runtime.sendMessage(msg, (value) => {
      const err = api.runtime.lastError;
      if (err) {
        reject(new Error(err.message));
      } else {
        resolve(value);
      }
    });
  });
}

function setStatus(text, ok) {
  const el = document.getElementById("status");
  el.textContent = text;
  el.classList.toggle("ok", ok === true);
  el.classList.toggle("err", ok === false);
}

async function runHandoff() {
  setStatus("正在发送…", null);
  try {
    const result = await sendMessage({ type: "handoff" });
    setStatus(result && result.title ? result.title : "已发送", result ? result.ok : true);
  } catch (err) {
    setStatus(`发送失败：${err.message}`, false);
  }
}

async function init() {
  const stored = await api.storage.local.get({ port: DEFAULT_PORT, lastResult: null });
  const portInput = document.getElementById("port");
  portInput.value = stored.port;
  if (stored.lastResult && stored.lastResult.title) {
    setStatus(stored.lastResult.title, stored.lastResult.ok);
  }

  portInput.addEventListener("change", async () => {
    const port = Number.parseInt(portInput.value, 10);
    if (port >= 1 && port <= 65535) {
      await api.storage.local.set({ port });
    }
  });
  document.getElementById("send").addEventListener("click", runHandoff);

  await runHandoff();
}

document.addEventListener("DOMContentLoaded", init);
