// Regression tests from the adversarial review of the back office's pure
// helpers.
import { test } from "node:test";
import assert from "node:assert/strict";
import { isUUID } from "./ids.ts";
import { formatPaise, formatRate, paiseToInput, parseRate, parseRupees } from "./money.ts";
import { billedValue, lineValue, openValue, orderValue } from "./orders.ts";
import type { Order, OrderLine } from "./types.ts";

test("parseRate accepts a space before the percent sign ('5 %', '18 %')", () => {
  // The GST form's placeholder is "5"; people type "5 %" too. trim() runs
  // before the "%" is stripped, so the space it leaves behind fails the regex.
  assert.equal(parseRate("5 %"), 500);
  assert.equal(parseRate("18 %"), 1800);
});

const line = (over: Partial<OrderLine>): OrderLine => ({
  id: "l",
  name: "x",
  qty: 1,
  unit_price_paise: 0,
  tax_rate_bp: 500,
  course: 1,
  status: "fired",
  added_at: "2026-10-02T12:00:00Z",
  ...over,
});

test("lineValue is qty x (unit + modifiers), like the pricing engine's gross", () => {
  const l = line({ qty: 3, unit_price_paise: 32000, modifiers: [{ name: "cheese", price_paise: 4000 }, { name: "butter", price_paise: 2000 }] });
  assert.equal(lineValue(l), 3 * (32000 + 6000));
  assert.equal(lineValue(line({ qty: 2, unit_price_paise: 9500 })), 19000);
});

test("orderValue counts billed lines, so the live floor shows openValue", () => {
  const o = {
    lines: [line({ unit_price_paise: 10000, status: "billed" }), line({ unit_price_paise: 10000, status: "fired" }), line({ unit_price_paise: 999, status: "voided" })],
  } as Order;
  assert.equal(orderValue(o), 20000); // what floor/page.tsx shows on the card and in "On open orders"
  assert.equal(billedValue(o), 10000); // already in today's Sales
  assert.equal(openValue(o), 10000); // what is actually still open
});

test("money parsing and formatting edge cases", () => {
  assert.equal(parseRupees("1,00,000"), 10000000);
  assert.equal(parseRupees("₹ 0"), 0);
  assert.equal(parseRupees("000000000.99"), 99);
  for (const bad of ["Rs 5", "1 200", "٣٢٠", "１２０", "0x10", "+5", "₹-5", "NaN", "Infinity"]) {
    assert.equal(parseRupees(bad), null, bad);
  }
  assert.equal(formatPaise(-0), "₹0.00");
  assert.equal(formatPaise(Number.MAX_SAFE_INTEGER + 1), "₹—");
  assert.equal(formatRate(5), "0.05%");
  assert.equal(formatRate(50), "0.5%");
  assert.equal(paiseToInput(5), "0.05");
  assert.equal(parseRate("40.00"), 4000);
  assert.equal(parseRate("40.01"), null);
});

test("isUUID refuses anything that could steer an API path", () => {
  assert.ok(isUUID("01A0FDAB-2CFD-7A06-8BEE-4DA44DA6ABF5")); // Go's uuid.Parse accepts upper case too
  for (const bad of [
    "01a0fdab-2cfd-7a06-8bee-4da44da6abf5\n",
    "01a0fdab-2cfd-7a06-8bee-4da44da6abf5?x=1",
    "01a0fdab-2cfd-7a06-8bee-4da44da6abf5#",
    "%2e%2e%2f",
    "{01a0fdab-2cfd-7a06-8bee-4da44da6abf5}",
    ["01a0fdab-2cfd-7a06-8bee-4da44da6abf5"],
    { toString: () => "01a0fdab-2cfd-7a06-8bee-4da44da6abf5" },
  ]) {
    assert.equal(isUUID(bad), false, String(bad));
  }
});
