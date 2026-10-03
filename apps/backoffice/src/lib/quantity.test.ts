import { test } from "node:test";
import assert from "node:assert/strict";
import { formatQty, parseQty, qtyToFriendlyInput, qtyToInput } from "./quantity.ts";
import { newDocId, isUUID } from "./ids.ts";

test("formatQty shows small amounts in the small unit", () => {
  assert.equal(formatQty(850, "kg"), "850 g");
  assert.equal(formatQty(1250, "kg"), "1.25 kg");
  assert.equal(formatQty(0, "kg"), "0 kg");
  assert.equal(formatQty(250, "l"), "250 ml");
  assert.equal(formatQty(15000, "l"), "15 l");
  assert.equal(formatQty(2000, "pc"), "2 pc");
  assert.equal(formatQty(500, "pc"), "0.5 pc");
  assert.equal(formatQty(-12000, "pc"), "−12 pc");
  assert.equal(formatQty(123456789, "kg"), "1,23,456.789 kg");
});

test("parseQty reads the item's unit or the smaller one, exactly", () => {
  assert.equal(parseQty("1.5", "kg"), 1500);
  assert.equal(parseQty("1.5 kg", "kg"), 1500);
  assert.equal(parseQty("250 g", "kg"), 250);
  assert.equal(parseQty("250g", "kg"), 250);
  assert.equal(parseQty("1,200 g", "kg"), 1200);
  assert.equal(parseQty("0.333", "l"), 333);
  assert.equal(parseQty("750 ml", "l"), 750);
  assert.equal(parseQty("12", "pc"), 12000);
  assert.equal(parseQty("0.5 pc", "pc"), 500);
  assert.equal(parseQty("0", "kg"), 0);
  for (const bad of ["", "-1", "1.2345", "2.5 g", "1 l", "abc", "1/2", "1e3", "250 ml"]) {
    assert.equal(parseQty(bad, "kg"), null, bad);
  }
});

test("qtyToInput round-trips through parseQty", () => {
  for (const q of [0, 1, 80, 1250, 1500000, 999]) {
    assert.equal(parseQty(qtyToInput(q), "kg"), q);
  }
});

test("newDocId makes distinct UUIDv7s", () => {
  const a = newDocId(), b = newDocId();
  assert.ok(isUUID(a) && a !== b);
  assert.equal(a[14], "7");
});

test("qtyToFriendlyInput uses grams and millilitres under one unit, and parseQty reads it back", () => {
  assert.equal(qtyToFriendlyInput(15, "kg"), "15 g");
  assert.equal(qtyToFriendlyInput(250, "l"), "250 ml");
  assert.equal(qtyToFriendlyInput(1500, "kg"), "1.5");
  assert.equal(qtyToFriendlyInput(500, "pc"), "0.5");
  for (const [q, u] of [[15, "kg"], [999, "l"], [1500, "kg"], [500, "pc"], [12000, "pc"]] as const) {
    assert.equal(parseQty(qtyToFriendlyInput(q, u), u), q);
  }
});
