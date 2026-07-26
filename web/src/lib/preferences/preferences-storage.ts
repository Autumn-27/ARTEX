"use client";

import { setClientCookie } from "../cookie.client";
import { setLocalStorageValue } from "../local-storage.client";
import { PREFERENCE_PERSISTENCE, type PreferenceKey } from "./preferences-config";

export async function persistPreference(key: PreferenceKey, value: string) {
  const mode = PREFERENCE_PERSISTENCE[key];

  switch (mode) {
    case "none":
      return;

    // 静态导出无 Node 服务端，server-cookie 退化为浏览器 cookie（写入语义等价）。
    case "server-cookie":
    case "client-cookie":
      setClientCookie(key, value);
      return;

    case "localStorage":
      setLocalStorageValue(key, value);
      return;

    default:
      return;
  }
}
