import { mergeActivities } from "./activity-merge.ts";
import assert from "node:assert/strict";
import test from "node:test";

const activity = (seq, kind = "tool_use", extra = {}) => ({
  seq,
  kind,
  worker: "auto",
  ts: "2026-09-10T07:00:00Z",
  summary: String(seq),
  ...extra,
});

test("overlapping poll responses render tool 198 only once", () => {
  const first = [activity(197), activity(198)];
  const second = [activity(198), activity(199, "tool_result")];
  const merged = mergeActivities(mergeActivities([], first), second);
  assert.deepEqual(
    merged.map((item) => item.seq),
    [197, 198, 199],
  );
  assert.deepEqual(mergeActivities(merged, second), merged);
});

test("a late response and overlapping older page preserve newer messages", () => {
  let rows = mergeActivities([activity(198)], [activity(199), activity(200)]);
  rows = mergeActivities(rows, [activity(198), activity(199)]);
  rows = mergeActivities([activity(196), activity(197), activity(198)], rows);
  assert.deepEqual(
    rows.map((item) => item.seq),
    [196, 197, 198, 199, 200],
  );
});

test("replayed results and duplicate entries do not double-count usage", () => {
  const result = activity(201, "result", { input_tokens: 120, output_tokens: 30 });
  const rows = mergeActivities([result, result], [result, result]);
  assert.equal(rows.length, 1);
  assert.equal(
    rows.reduce((sum, item) => sum + item.input_tokens, 0),
    120,
  );
});

test("merging does not mutate either response and keeps distinct IDs", () => {
  const first = Object.freeze([Object.freeze(activity(199))]);
  const second = Object.freeze([Object.freeze(activity(198)), Object.freeze(activity(200))]);
  assert.deepEqual(
    mergeActivities(first, second).map((item) => item.seq),
    [198, 199, 200],
  );
  assert.equal(first.length, 1);
  assert.equal(second.length, 2);
});
