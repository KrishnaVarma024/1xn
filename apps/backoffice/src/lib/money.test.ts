import { test } from "node:test";
import assert from "node:assert/strict";
import { formatPaise, formatPrice, formatRate, paiseToInput, parseRate, parseRupees } from "./money.ts";

test("formatPaise uses Indian grouping and never floats", () => {
  assert.equal(formatPaise(0), "₹0.00");
  assert.equal(formatPaise(9500), "₹95.00");
  assert.equal(formatPaise(1234567), "₹12,345.67");
  assert.equal(formatPaise(12345678901), "₹12,34,56,789.01");
  assert.equal(formatPaise(-24), "−₹0.24");
  assert.equal(formatPaise(0.5), "₹—");
});

test("formatPrice drops .00 only", () => {
  assert.equal(formatPrice(32000), "₹320");
  assert.equal(formatPrice(32050), "₹320.50");
});

test("parseRupees reads what people type", () => {
  assert.equal(parseRupees("320"), 32000);
  assert.equal(parseRupees("320.5"), 32050);
  assert.equal(parseRupees("320.05"), 32005);
  assert.equal(parseRupees(" ₹1,200.00 "), 120000);
  assert.equal(parseRupees("0.1"), 10);
  // 0.1 + 0.2 style float bugs cannot happen: the parse is string math.
  assert.equal(parseRupees("0.29"), 29);
  for (const bad of ["", "-5", "1.234", "abc", "1e5", "12.", ".5", "9999999999"]) {
    assert.equal(parseRupees(bad), null, bad);
  }
});

test("paiseToInput round-trips", () => {
  for (const p of [0, 5, 95, 9500, 32050, 123456789]) {
    assert.equal(parseRupees(paiseToInput(p)), p);
  }
});

test("rates in basis points", () => {
  assert.equal(formatRate(500), "5%");
  assert.equal(formatRate(1250), "12.5%");
  assert.equal(formatRate(1205), "12.05%");
  assert.equal(parseRate("5"), 500);
  assert.equal(parseRate("12.5%"), 1250);
  assert.equal(parseRate("41"), null);
  assert.equal(parseRate("-1"), null);
});
