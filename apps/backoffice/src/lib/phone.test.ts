import { test } from "node:test";
import assert from "node:assert/strict";
import { formatPhone, normalisePhone } from "./phone.ts";

test("Indian mobile numbers become E.164; anything unclear is refused", () => {
  for (const ok of ["98765 43210", "098765-43210", "91 98765 43210", "(98765) 43210"]) {
    assert.equal(normalisePhone(ok), "+919876543210", ok);
  }
  assert.equal(normalisePhone("+44 7700 900123"), "+447700900123");
  for (const bad of ["", "12345", "58765 43210", "98765 4321", "9876543210x", "919876543210 1"]) {
    assert.equal(normalisePhone(bad), null, bad);
  }
});

test("phones read the way they are said, and read back the same", () => {
  assert.equal(formatPhone("+919876543210"), "98765 43210");
  assert.equal(formatPhone("+447700900123"), "+447700900123");
  assert.equal(formatPhone("+912223456789"), "+912223456789"); // a Mumbai landline keeps its +91
  for (const stored of ["+919876543210", "+912223456789", "+911800123456", "+447700900123"]) {
    assert.equal(normalisePhone(formatPhone(stored)), stored, stored);
  }
});
