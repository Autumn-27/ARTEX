// safeHref 校验一个可进入 href 的 URL,只允许 http: / https: scheme。
//
// 背景:资产 URL、GitHub release 地址等数据来自后端/目标侧,直接塞进 <a href>
// 会让 javascript:、data: 之类的伪协议变成可点链接(XSS 入口)。凡是非编译期
// 常量的 href 都必须先过这道校验;返回 undefined 时调用方应降级渲染为纯文本。
//
// 注意:URL 构造器对相对串(如 "/path" 或空协议相对串 "//host")要求 base,
// 这里故意不提供 base —— 相对/畸形输入一律视为不可信,返回 undefined。
export function safeHref(raw: string | null | undefined): string | undefined {
  if (!raw) return undefined;
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  // 控制字符与空白可嵌在 scheme 里绕过朴素前缀检查(如 "java\tscript:"),
  // 浏览器解析 href 时会忽略它们,所以先剥掉再校验。
  // biome-ignore lint/suspicious/noControlCharactersInRegex: 刻意匹配控制字符以识别伪装 scheme
  const cleaned = trimmed.replace(/[\u0000-\u0020]+/g, "");
  let parsed: URL;
  try {
    parsed = new URL(cleaned);
  } catch {
    return undefined;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
  return trimmed;
}
