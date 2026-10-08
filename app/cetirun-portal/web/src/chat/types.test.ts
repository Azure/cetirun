// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, it, expect } from "vitest";
import { buildContext, byteLength, LIMITS, type Turn } from "./types";
import { EventParser } from "./api";

const turn = (id: string, resultState: Turn["resultState"] = "completed") =>
  ({ id, resultState }) as Turn;
describe("chat context", () => {
  it("uses text history without native session IDs or failed turns", () => {
    const result = buildContext(
      [
        { turn: turn("one"), user: "First", assistant: "Answer" },
        {
          turn: turn("failed", "interrupted"),
          user: "Unknown action",
          assistant: "Partial",
        },
      ],
      "Next",
      "agent",
    );
    expect(result.messages).toEqual([
      { role: "user", content: "First" },
      { role: "assistant", content: "Answer" },
      { role: "user", content: "Next" },
    ]);
    expect(result.turnIds).toEqual(["one"]);
  });
  it("keeps at most 49 complete pairs and reports omitted history", () => {
    const result = buildContext(
      Array.from({ length: 60 }, (_, i) => ({
        turn: turn(String(i)),
        user: `Question ${i}`,
        assistant: `Answer ${i}`,
      })),
      "Next",
      "agent",
    );
    expect(result.messages).toHaveLength(99);
    expect(result.omitted).toBe(11);
    expect(result.turnIds[0]).toBe("11");
  });
  it("bounds exact encoded bytes and never jumps over a too-large recent pair", () => {
    const history = [
      { turn: turn("old"), user: "old", assistant: "old" },
      { turn: turn("new"), user: "new", assistant: "中".repeat(400000) },
    ];
    const result = buildContext(history, "Next", "agent");
    expect(result.messages).toHaveLength(1);
    expect(result.omitted).toBe(2);
    expect(history[1].assistant.length).toBe(400000);
    expect(
      byteLength(
        JSON.stringify({
          model: "agent",
          messages: result.messages,
          stream: true,
        }),
      ),
    ).toBeLessThan(LIMITS.requestBytes);
  });
  it("rejects oversized or blank current input without silently shortening it", () => {
    expect(() => buildContext([], " ", "agent")).toThrow();
    expect(() => buildContext([], "中".repeat(22000), "agent")).toThrow();
  });
});
describe("SSE framing", () => {
  it("preserves split CRLF and multi-line data", () => {
    const parser = new EventParser();
    expect(parser.push(": heartbeat\r\ndata: {\r")).toEqual([]);
    expect(parser.push('\ndata: "a":1}\r\n\r\n')).toEqual(['{\n"a":1}']);
    expect(parser.push("data: [DONE]\n\n")).toEqual(["[DONE]"]);
  });
  it("does not mistake a network chunk for a complete event", () => {
    const parser = new EventParser();
    expect(parser.push("data: hello")).toEqual([]);
    expect(parser.push(" world\n\n")).toEqual(["hello world"]);
  });
  it("bounds unfinished events", () => {
    expect(() => new EventParser().push("x".repeat(256 * 1024 + 1))).toThrow();
  });
});
