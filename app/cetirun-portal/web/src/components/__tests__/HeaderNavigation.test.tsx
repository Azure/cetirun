// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Header } from "../Header";
import * as MeContext from "../../context/MeContext";
import * as ThemeContext from "../../context/ThemeContext";

vi.mock("../../hooks/useFeatures", () => ({ useFeatures: () => ({ data: { agentToolsEnabled: true } }) }));
afterEach(() => vi.restoreAllMocks());

function renderHeader(path: string, themeMode: ThemeContext.ThemeMode = "system") {
  const setThemeMode = vi.fn();
  vi.spyOn(ThemeContext, "useThemeContext").mockReturnValue({ themeMode, setThemeMode });
  vi.spyOn(MeContext, "useMe").mockReturnValue({
    me: { alias: "alice", displayName: "Alice", email: "alice@example.com", isAdmin: true },
    isLoading: false,
    error: null,
  });
  render(<MemoryRouter initialEntries={[path]}><Header /></MemoryRouter>);
  return setThemeMode;
}

describe("Header navigation", () => {
  it.each(["/", "/instances/alice/overview", "/instances/alice/chat"])("marks Agents as the current page at %s", (path) => {
    renderHeader(path);
    expect(screen.getByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");
  });

  it.each([
    ["/connections", "Connections"],
    ["/admin/teams", "Admin"],
  ])("marks only %s's own entry as the current page", (path, current) => {
    renderHeader(path);
    const navigation = within(screen.getByRole("navigation", { name: "Main navigation" }));
    expect(navigation.getByRole("link", { name: "Agents" })).not.toHaveAttribute("aria-current");
    expect(navigation.getByRole("link", { name: current })).toHaveAttribute("aria-current", "page");
  });

  it("links Agents to the agent list", () => {
    renderHeader("/admin/teams");
    expect(screen.getByRole("link", { name: "Agents" })).toHaveAttribute("href", "/");
  });

  it("shows the alias of the account that is signed in", () => {
    renderHeader("/");
    expect(screen.getByText("alice")).toBeInTheDocument();
  });
});

describe("Header theme", () => {
  it("checks the theme in use", () => {
    renderHeader("/", "light");
    const theme = within(screen.getByRole("radiogroup", { name: "Theme" }));
    expect(theme.getByRole("radio", { name: "Light" })).toBeChecked();
    expect(theme.getByRole("radio", { name: "System" })).not.toBeChecked();
    expect(theme.getByRole("radio", { name: "Dark" })).not.toBeChecked();
  });

  it.each([
    ["System", "system"],
    ["Dark", "dark"],
  ])("selects %s", (label, mode) => {
    const setThemeMode = renderHeader("/", "light");
    fireEvent.click(screen.getByRole("radio", { name: label }));
    expect(setThemeMode).toHaveBeenCalledExactlyOnceWith(mode);
  });
});
