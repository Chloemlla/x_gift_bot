import test from "node:test";
import assert from "node:assert/strict";
import { isPaymentResult } from "../src/manualPaymentResult.ts";
const plan = { months: 3, amount: 30000, currency: "BDT" };
const valid = { ...plan, username: "recipient", status: "created", checkout_url: "https://checkout.stripe.com/c/pay/cs_test_Synthetic" };
test("queue acknowledgements and malformed successes never become payment results", () => {
  for (const value of [null, {}, { ticket: "ticket", status: "queued" }, { status: "processing", ticket: "ticket" }, { message: "ok" }, { ...valid, amount: undefined }, { ...valid, amount: NaN }, { ...valid, checkout_url: undefined }, { ...valid, username: "another" }, { ...valid, months: 6 }, { ...valid, currency: "USD" }, { ...valid, checkout_url: "https://example.com/pay" }, { ...valid, checkout_url: "javascript:alert(1)" }]) {
    assert.equal(isPaymentResult(value, "recipient", plan), false);
  }
});
test("complete correct orders and already-paid outcomes are accepted", () => {
  assert.equal(isPaymentResult(valid, "recipient", plan), true);
  assert.equal(isPaymentResult({ ...valid, status: "requires_action" }, "recipient", plan), true);
  assert.equal(isPaymentResult({ ...valid, status: "succeeded", checkout_url: undefined }, "recipient", plan), true);
});
