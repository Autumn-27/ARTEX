// F6: 401 → refresh-and-retry 的单例 refresh。
// access token(2h,localStorage)过期后,凭 HttpOnly refresh cookie(14d,
// Path=/api/auth)向 /api/auth/refresh 换新 token;并发 401 共享同一次
// refresh 请求,避免多个请求同时旋转 refresh token 互相作废。

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

let refreshPromise: Promise<boolean> | null = null;

// refreshAccessToken 尝试用 refresh cookie 换新 access token 并写回
// localStorage;返回 false 表示 refresh 也失效(应清登录态回 /login)。
// fetchImpl 可注入以便 node:test 单测。
export function refreshAccessToken(fetchImpl: FetchLike = fetch): Promise<boolean> {
  if (!refreshPromise) {
    refreshPromise = (async () => {
      try {
        const r = await fetchImpl("/api/auth/refresh", { method: "POST" });
        if (!r.ok) return false;
        const data = (await r.json()) as { token?: unknown };
        if (typeof data.token !== "string" || !data.token) return false;
        localStorage.setItem("artex_token", data.token);
        return true;
      } catch {
        return false;
      }
    })().finally(() => {
      refreshPromise = null;
    });
  }
  return refreshPromise;
}
