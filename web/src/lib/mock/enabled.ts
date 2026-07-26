// Mock 开关。构建期注入的公开变量（NEXT_PUBLIC_ 前缀才在浏览器可读）。
// Vercel 上设 NEXT_PUBLIC_MOCK=1 即整站走 mock、无需后端。
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
