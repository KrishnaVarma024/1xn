import { test } from "node:test";
import assert from "node:assert/strict";
import { fyCode, isRealDay, longDate, shiftDay } from "./dates.ts";

test("isRealDay keeps real days from 2017 to 2100", () => {
  assert.equal(isRealDay("2026-09-28"), true);
  assert.equal(isRealDay("2026-02-30"), false);
  assert.equal(isRealDay("0000-01-01"), false);
  assert.equal(isRealDay("2016-12-31"), false);
  assert.equal(isRealDay("28-09-2026"), false);
  assert.equal(isRealDay(["2026-09-28"]), false);
});

test("longDate names the trading day itself, not the day in the server's zone", () => {
  assert.equal(longDate("2026-09-28"), "Monday, 28 September 2026");
});

test("shiftDay crosses months and years", () => {
  assert.equal(shiftDay("2026-09-30", 1), "2026-10-01");
  assert.equal(shiftDay("2027-01-01", -1), "2026-12-31");
});

test("fyCode follows India's financial year, in India time", () => {
  assert.equal(fyCode(new Date("2026-10-03T12:00:00Z")), "2627");
  assert.equal(fyCode(new Date("2027-03-31T18:00:00Z")), "2627"); // 23:30 IST on 31 March
  assert.equal(fyCode(new Date("2027-03-31T19:00:00Z")), "2728"); // 00:30 IST on 1 April
  assert.equal(fyCode(new Date("2099-06-01T00:00:00Z")), "9900");
});
