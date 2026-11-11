// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

const messages = {
  tool_budget_exceeded: "Pi reached its eight-tool-call limit. Earlier tool actions may have completed.",
  deadline_exceeded: "Pi reached its two-minute task deadline. Earlier tool actions may have completed.",
  output_limit_exceeded: "Pi reached its response output limit. Earlier tool actions may have completed.",
  model_limits_unavailable: "Pi cannot use this Gateway model until trusted model limits are available.",
  context_budget_exceeded: "Pi cannot send this request because its context leaves no approved response budget.",
  run_failed: "Pi run failed or model access is unavailable",
};

export class RunError extends Error {
  constructor(code) {
    if (!Object.hasOwn(messages, code)) throw new Error("Invalid Pi run error code");
    super(messages[code]);
    this.code = code;
  }
}
