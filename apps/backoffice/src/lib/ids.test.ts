import { test } from "node:test";
import assert from "node:assert/strict";
import { isUUID, mustUUID } from "./ids.ts";

test("only canonical UUIDs pass", () => {
  assert.ok(isUUID("01a0fdab-2cfd-7a06-8bee-4da44da6abf5"));
  for (const bad of ["", "../ledger/trial-balance", "01a0fdab-2cfd-7a06-8bee-4da44da6abf5/../x", "01a0fdab2cfd7a068bee4da44da6abf5", 42, null]) {
    assert.equal(isUUID(bad), false, String(bad));
  }
  assert.throws(() => mustUUID("x/../y"));
});
