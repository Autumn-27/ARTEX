import { safeHref } from "./safe-href.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("safeHref allows http/https URLs", () => {
  assert.equal(safeHref("https://example.com/path?q=1"), "https://example.com/path?q=1");
  assert.equal(safeHref("http://192.168.1.1:8080"), "http://192.168.1.1:8080");
  assert.equal(safeHref("  https://example.com  "), "https://example.com");
});

test("safeHref rejects pseudo schemes and malformed input", () => {
  assert.equal(safeHref("javascript:alert(1)"), undefined);
  assert.equal(safeHref("JavaScript:alert(1)"), undefined);
  assert.equal(safeHref("data:text/html,<script>1</script>"), undefined);
  assert.equal(safeHref("vbscript:msgbox(1)"), undefined);
  assert.equal(safeHref("file:///etc/passwd"), undefined);
  // 控制字符嵌在 scheme 里的伪装形式。
  assert.equal(safeHref("java\tscript:alert(1)"), undefined);
  assert.equal(safeHref("java\nscript:alert(1)"), undefined);
});

test("safeHref rejects relative / protocol-relative / empty input", () => {
  assert.equal(safeHref("//evil.example/x"), undefined);
  assert.equal(safeHref("/internal/path"), undefined);
  assert.equal(safeHref("example.com"), undefined);
  assert.equal(safeHref(""), undefined);
  assert.equal(safeHref("   "), undefined);
  assert.equal(safeHref(null), undefined);
  assert.equal(safeHref(undefined), undefined);
});
