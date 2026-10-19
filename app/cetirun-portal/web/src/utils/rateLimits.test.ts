// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { describeRateLimits, emptyRateLimitForm, rateLimitFormFrom, rateLimitsFromForm } from "./rateLimits";

describe("rate limit form", () => {
  it("leaves empty inputs out instead of sending zero", () => {
    expect(rateLimitsFromForm(emptyRateLimitForm)).toEqual({});
    expect(rateLimitsFromForm({ ...emptyRateLimitForm, rpm: " 60 ", perModel: [{ model: "", rpm: "", tpm: "" }] })).toEqual({
      limits: { rpm: 60 },
    });
  });

  it("builds totals and per-model limits", () => {
    const form = {
      maxParallelRequests: "4", rpm: "60", tpm: "200000",
      perModel: [{ model: "example-model", rpm: "10", tpm: "" }, { model: "auto", rpm: "", tpm: "8192" }],
    };
    const limits = { maxParallelRequests: 4, rpm: 60, tpm: 200000, perModel: { "example-model": { rpm: 10 }, auto: { tpm: 8192 } } };
    expect(rateLimitsFromForm(form)).toEqual({ limits });
    expect(rateLimitsFromForm(rateLimitFormFrom(limits))).toEqual({ limits });
  });

  it.each([
    [{ ...emptyRateLimitForm, maxParallelRequests: "0" }, "at least 1"],
    [{ ...emptyRateLimitForm, rpm: "-5" }, "whole number"],
    [{ ...emptyRateLimitForm, rpm: "1.5" }, "whole number"],
    [{ ...emptyRateLimitForm, tpm: "4095" }, "at least 4,096"],
    [{ ...emptyRateLimitForm, tpm: "99999999999999999999" }, "too large"],
    [{ ...emptyRateLimitForm, perModel: [{ model: "a model", rpm: "1", tpm: "" }] }, "without spaces"],
    [{ ...emptyRateLimitForm, perModel: [{ model: "", rpm: "1", tpm: "" }] }, "model name"],
    [{ ...emptyRateLimitForm, perModel: [{ model: "m", rpm: "", tpm: "" }] }, "Set requests or tokens"],
    [{ ...emptyRateLimitForm, perModel: [{ model: "m", rpm: "0", tpm: "" }] }, "at least 1"],
    [{ ...emptyRateLimitForm, perModel: [{ model: "m", rpm: "", tpm: "100" }] }, "at least 4,096"],
    [{ ...emptyRateLimitForm, perModel: [{ model: "m", rpm: "1", tpm: "" }, { model: "m", rpm: "2", tpm: "" }] }, "more than once"],
  ])("rejects %j", (form, message) => {
    const result = rateLimitsFromForm(form);
    expect(result.limits).toBeUndefined();
    expect(result.error).toContain(message);
  });

  it("describes limits for read-only views", () => {
    expect(describeRateLimits(undefined)).toEqual([]);
    expect(describeRateLimits({})).toEqual([]);
    expect(describeRateLimits({ maxParallelRequests: 4, tpm: 200000, perModel: { "example-model": { rpm: 10, tpm: 50000 } } })).toEqual([
      "Up to 4 requests at a time",
      "200,000 tokens per minute",
      "example-model: 10 requests per minute, 50,000 tokens per minute",
    ]);
  });
});
