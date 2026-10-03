// Adversarial review of the stock slice's quantity parsing. Each test names a
// defect and fails against the code as reviewed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { parseQty } from "./quantity.ts";
import { parseRupees } from "./money.ts";

// S11. A comma is read as a thousands separator wherever it stands, so a
// decimal comma (phone keyboards in many locales, and a common way to write
// "one and a half") is silently read 100 times too big: "1,50" kg of paneer
// wasted books 150 kg, a count of "2,50" kg sets the books to 250 kg. ("2,5"
// is refused, which makes the two-digit case more surprising.) Neither
// "1,50" nor "10,00" is a valid Indian or Western grouping (the last group
// always has three digits), so they must be refused, not guessed.
test("S11 parseQty refuses a decimal comma instead of reading it as thousands", () => {
  for (const s of ["1,50", "1,50 kg", "2,50", "10,00", "1,05 kg", "12,345,67"]) {
    const got = parseQty(s, "kg");
    assert.ok(got === null || got < 100_000, `parseQty(${JSON.stringify(s)}, "kg") = ${got} milli-units (${(got ?? 0) / 1000} kg)`);
  }
  // Real groupings still read.
  assert.equal(parseQty("1,500 g", "kg"), 1500);
  assert.equal(parseQty("1,25,000 g", "kg"), 125000);
  assert.equal(parseQty("12,500", "pc"), 12500000);
});

// S11b. The receive form reads each line's cost with parseRupees, which drops
// every comma: "1,50" (one rupee fifty) is booked as Rs 150.
test("S11b parseRupees (used for purchase costs) refuses a decimal comma", () => {
  for (const s of ["1,50", "12,5", "4,50"]) {
    const got = parseRupees(s);
    assert.ok(got === null || got < 1_000, `parseRupees(${JSON.stringify(s)}) = ${got} paise`);
  }
  assert.equal(parseRupees("1,200.50"), 120050);
  assert.equal(parseRupees("1,20,000"), 12000000);
});
