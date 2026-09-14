// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Header } from "../Header";
import * as MeContext from "../../context/MeContext";
import * as ThemeContext from "../../context/ThemeContext";

function mockTheme() {
  vi.spyOn(ThemeContext, "useThemeContext").mockReturnValue({
    themeMode: "system",
    setThemeMode: vi.fn(),
  });
}

describe("Header admin link", () => {
  it("renders the CetiRun name and logo", () => {
    mockTheme();
    vi.spyOn(MeContext, "useMe").mockReturnValue({
      me: undefined,
      isLoading: false,
      error: null,
    });
    render(
      <MemoryRouter>
        <Header />
      </MemoryRouter>,
    );
    expect(screen.getByText("CetiRun Portal")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "CetiRun" })).toHaveAttribute(
      "src",
      expect.stringContaining("cetirun.png"),
    );
  });

  it("does not render Admin link for non-admin user", () => {
    mockTheme();
    vi.spyOn(MeContext, "useMe").mockReturnValue({
      me: { alias: "bob", displayName: "Bob", email: "b@x", isAdmin: false },
      isLoading: false,
      error: null,
    });
    render(
      <MemoryRouter>
        <Header />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("link", { name: "Admin" })).toBeNull();
  });

  it("renders Admin link for admin user", () => {
    mockTheme();
    vi.spyOn(MeContext, "useMe").mockReturnValue({
      me: { alias: "alice", displayName: "Alice", email: "a@x", isAdmin: true },
      isLoading: false,
      error: null,
    });
    render(
      <MemoryRouter>
        <Header />
      </MemoryRouter>,
    );
    const link = screen.getByRole("link", { name: "Admin" });
    expect(link).toHaveAttribute("href", "/admin");
  });

  it("does not render Admin link when me is unavailable", () => {
    mockTheme();
    vi.spyOn(MeContext, "useMe").mockReturnValue({
      me: undefined,
      isLoading: false,
      error: null,
    });
    render(
      <MemoryRouter>
        <Header />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("link", { name: "Admin" })).toBeNull();
  });
});
