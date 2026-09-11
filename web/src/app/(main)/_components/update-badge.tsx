"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * 顶栏的"有新版本"提示：整页加载时查一次，有更新就在版本号旁边亮出来，
 * 点击直达系统配置页的「版本与更新」卡片。
 *
 * 后端对 GitHub 的查询结果有 30 分钟缓存，所以这里每次挂载都查一次是安全的
 * ——未认证的 GitHub API 只有 60 次/小时/IP，没有那层缓存的话，多开几个标签页
 * 就会把配额耗光，之后真想更新反而查不动。
 *
 * 查询失败一律静默：顶栏不是报错的地方，用户进设置页点「检查更新」会看到原因。
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update 已经包含了"版本号可比较"的判断，开发构建不会亮这个提示。
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // 静默：没网 / GitHub 限流都不该在顶栏弹错误。
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`发现新版本 ${latest}，点击前往更新`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* 呼吸点：顶栏元素很多，纯文字容易被忽略，动效让它一眼可见。 */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">新版本 {latest}</span>
      <span className="sm:hidden">新版本</span>
    </Link>
  );
}
