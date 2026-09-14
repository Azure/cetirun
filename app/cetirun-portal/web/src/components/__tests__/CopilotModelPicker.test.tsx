import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { CopilotModelPicker } from "../CopilotModelPicker";
import type { Instance } from "../../types/instance";

const mock = vi.hoisted(() => ({
  supported: true,
  result: {
    data: { models: [] },
    isLoading: false,
    isError: false,
    isFetching: false,
    refetch: vi.fn(),
  },
}));

vi.mock("../../hooks/useCopilotAuth", () => ({
  isCopilotAuthSupported: () => mock.supported,
  useCopilotModelsQuery: () => mock.result,
}));

const instance: Instance = {
  instanceName: "alice",
  agentType: "openclaw",
  alias: "alice",
  displayName: "Alice",
  team: "",
  phase: "Ready",
  accessUrl: "https://cetirun.example.com/alice",
  cetirunAuthKey: "test-key",
  createdAt: "2026-01-01T00:00:00Z",
  upgradeAvailable: false,
};

function StatefulPicker({ initialValue, onChange }: { initialValue: string; onChange: (value: string) => void }) {
  const [value, setValue] = useState(initialValue);
  return (
    <CopilotModelPicker
      instance={instance}
      value={value}
      onChange={(next) => {
        setValue(next);
        onChange(next);
      }}
    />
  );
}

describe("CopilotModelPicker", () => {
  beforeEach(() => {
    mock.supported = true;
    mock.result = {
      data: { models: [] },
      isLoading: false,
      isError: false,
      isFetching: false,
      refetch: vi.fn(),
    };
  });

  it("selects a live catalog model and refreshes the catalog", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mock.result.data = {
      models: [{ id: "gpt-6-astra", displayName: "GPT-6 Astra" }],
    };

    render(<CopilotModelPicker instance={instance} value="" onChange={onChange} allowDefault />);

    await user.selectOptions(screen.getByRole("combobox"), "gpt-6-astra");
    await user.selectOptions(screen.getByRole("combobox"), "");
    await user.click(screen.getByRole("button", { name: /refresh/i }));

    expect(onChange).toHaveBeenCalledWith("gpt-6-astra");
    expect(onChange).toHaveBeenLastCalledWith("");
    expect(mock.result.refetch).toHaveBeenCalledOnce();
  });

  it("keeps manual entry available and warns for an unknown model", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mock.result.data = {
      models: [{ id: "gpt-6-astra", displayName: "GPT-6 Astra" }],
    };

    render(<StatefulPicker initialValue="custom-model" onChange={onChange} />);

    expect(screen.getByText(/not in the current live catalog/i)).toBeInTheDocument();
    await user.type(screen.getByPlaceholderText("e.g. gpt-5.4"), "-next");
    expect(onChange).toHaveBeenLastCalledWith("custom-model-next");
  });

  it("reports a fetch error but does not claim an unsupported instance returned no models", () => {
    mock.result.isError = true;
    const { unmount } = render(<CopilotModelPicker instance={instance} value="" onChange={vi.fn()} />);
    expect(screen.getByText(/Live catalog unavailable/i)).toBeInTheDocument();

    unmount();
    mock.result.isError = false;
    mock.supported = false;
    render(<CopilotModelPicker instance={instance} value="" onChange={vi.fn()} />);
    expect(screen.queryByText(/No models were returned/i)).not.toBeInTheDocument();
  });
});
