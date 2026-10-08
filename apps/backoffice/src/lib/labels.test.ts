import { test } from "node:test";
import assert from "node:assert/strict";
import { dayIST } from "./labels.ts";

test("dayIST is the day in India, not in UTC", () => {
  assert.equal(dayIST("2026-10-07T20:00:00Z"), "2026-10-08"); // 1:30 am on the 8th in India
  assert.equal(dayIST("2026-10-07T18:29:59Z"), "2026-10-07");
});
