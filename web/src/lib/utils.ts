import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// 抽屉/对话框(Sheet/Dialog)的 onInteractOutside 关闭判定辅助。
//
// 背景:抽屉内的 Radix 弹层(Select 下拉、DropdownMenu、Popover 等)会 portal 到抽屉
// 之外。开着弹层时点遮罩/抽屉外想收起它,这一次 pointerdown 会被 Select 和 Sheet 两个
// DismissableLayer 同时处理;Select 先关闭且是 discrete 事件、React 会同步 flush,于是
// 轮到 Sheet 的处理器时弹层的 data-state 早已翻成 closed —— 在"当下"检测弹层是否打开
// 天然不可靠(实测已验证)。
//
// 正确做法:Radix 的 pointerdown 监听在冒泡阶段;我们在 capture 阶段(早于它)先把
// "此刻有没有弹层开着"记录下来,onInteractOutside 再读这个记录值来决定是否放行关闭。
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture:抢在 Radix 冒泡阶段的 pointerdown 处理器之前记录
  );
}

// radixOverlayWasOpenAtPointerDown 返回"最近一次 pointerdown 发生时是否有 Radix 弹层
// 开着"。抽屉/对话框据此:开着弹层时点遮罩 → 只收弹层、不关自身。
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
