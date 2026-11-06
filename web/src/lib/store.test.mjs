// The page's list logic has no framework and no DOM: store.js is plain modules,
// so it is tested with Node's built-in runner (`npm test`, no extra dependency).
//
// state is a module singleton, so every test starts by resetting the fields it
// touches. Each test also installs its own fetch stub: nothing here reaches the
// network.
import { test } from "node:test";
import assert from "node:assert/strict";

import { state, loadVideos } from "./store.js";

function json(body) {
  return { ok: true, status: 200, text: async () => JSON.stringify(body) };
}

function reset(offset) {
  state.offset = offset;
  state.total = 0;
  state.videos = [];
  state.logs.length = 0;
}

// stub installs a fetch that answers /api/videos from `page` and counts the
// requests. The cap turns a retry loop into a failure instead of a hang.
function stub(page) {
  const seen = [];
  globalThis.fetch = async (url) => {
    const u = new URL(url, "http://localhost");
    if (u.pathname === "/api/stats") {
      return json({});
    }
    seen.push(Number(u.searchParams.get("offset")));
    if (seen.length > 5) {
      throw new Error(`the list was requested ${seen.length} times, that is a retry loop`);
    }
    return json(page(Number(u.searchParams.get("offset"))));
  };
  return seen;
}

test("loadVideos: 慢的旧请求不得覆盖新请求", async () => {
  reset(100);
  const delays = { 100: 80, 200: 5 };
  stub((offset) => ({ total: 300, offset, items: [{ Bvid: `BV${offset}` }] }));
  const inner = globalThis.fetch;
  globalThis.fetch = async (url) => {
    const u = new URL(url, "http://localhost");
    if (u.pathname !== "/api/stats") {
      await new Promise((r) => setTimeout(r, delays[Number(u.searchParams.get("offset"))] ?? 0));
    }
    return inner(url);
  };

  const slow = loadVideos();
  state.offset = 200;
  const fast = loadVideos();
  await Promise.all([slow, fast]);

  assert.equal(state.videos[0]?.Bvid, "BV200", "第 2 页先返回，晚到的第 1 页不能盖掉它");
  assert.equal(state.offset, 200, "页码停在用户要去的那一页");
});

test("loadVideos: 总数与最后一页矛盾时只请求一次", async () => {
  reset(400);
  const seen = stub(() => ({ total: 450, offset: 400, items: [] }));

  await loadVideos();

  assert.equal(seen.length, 1, "重算出的页码与当前页码相同，不能无限重试");
  assert.equal(state.videos.length, 0, "拿到空页就显示空表");
  assert.equal(state.total, 450, "服务端报的总数照实显示，日志里另行说明");
  assert.ok(
    state.logs.some((line) => line.includes("列表对不上")),
    "要说清为什么表和总数对不上，而不是静默停住",
  );
});

test("loadVideos: 当前页越界时退到真正的最后一页", async () => {
  reset(500);
  const seen = stub((offset) => ({
    total: 250,
    offset,
    items: offset === 200 ? [{ Bvid: "BV200" }] : [],
  }));

  await loadVideos();

  assert.equal(state.offset, 200, "退到 floor((250-1)/100)*100");
  assert.equal(state.videos[0]?.Bvid, "BV200", "退一页后要真的拿到数据");
  assert.deepEqual(seen, [500, 200], "只多请求一次");
});

test("loadVideos: 请求失败时表格和分页器一起归零", async () => {
  reset(300);
  state.total = 300;
  globalThis.fetch = async (url) => {
    if (new URL(url, "http://localhost").pathname === "/api/stats") {
      return json({});
    }
    return { ok: false, status: 500, text: async () => JSON.stringify({ error: "boom" }) };
  };

  await loadVideos();

  assert.equal(state.videos.length, 0, "表清空");
  assert.equal(state.total, 0, "总数也归零，不能留着一个指向空表的旧数字");
  assert.equal(state.pager.text, "共 0 条", "分页器跟着表一起归零");
  assert.ok(
    state.logs.some((line) => line.includes("boom")),
    "把服务端的错误说明带到日志里",
  );
});
