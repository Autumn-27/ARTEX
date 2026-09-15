import { refreshAccessToken } from "./auth-refresh.ts";
import assert from "node:assert/strict";
import test from "node:test";

// auth-refresh.ts 在成功路径写 localStorage,node 里没有,给个最小 stub。
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  clear: () => store.clear(),
};

const okResponse = (token) => ({
  ok: true,
  json: async () => ({ token }),
});

test("refresh success writes the new access token to localStorage", async () => {
  store.clear();
  const ok = await refreshAccessToken(async () => okResponse("tok-new"));
  assert.equal(ok, true);
  assert.equal(store.get("artex_token"), "tok-new");
});

test("concurrent 401s share a single in-flight refresh (single-flight)", async () => {
  let calls = 0;
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  const fetchImpl = async () => {
    calls++;
    await gate; // 挂住第一次 refresh,让并发调用叠上来
    return okResponse("tok-shared");
  };
  const p1 = refreshAccessToken(fetchImpl);
  const p2 = refreshAccessToken(fetchImpl);
  const p3 = refreshAccessToken(fetchImpl);
  release();
  assert.deepEqual(await Promise.all([p1, p2, p3]), [true, true, true]);
  assert.equal(calls, 1);
});

test("failed refresh returns false and the next call retries fresh", async () => {
  let calls = 0;
  const fetchImpl = async () => {
    calls++;
    return { ok: false, json: async () => ({}) };
  };
  assert.equal(await refreshAccessToken(fetchImpl), false);
  // 上一次 promise 已结算并清空,再调应触发新的请求而不是复用失败结果。
  assert.equal(await refreshAccessToken(fetchImpl), false);
  assert.equal(calls, 2);
});

test("network throw returns false instead of rejecting", async () => {
  const ok = await refreshAccessToken(async () => {
    throw new Error("connection refused");
  });
  assert.equal(ok, false);
});

test("malformed payload (no token) returns false", async () => {
  const ok = await refreshAccessToken(async () => ({ ok: true, json: async () => ({}) }));
  assert.equal(ok, false);
});
