// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { afterEach, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { webDarkTheme, webLightTheme } from "@fluentui/react-components";
import { ThemeProvider } from "../ThemeContext";
import { portalTheme } from "../../theme/portalTheme";

describe("ThemeProvider", () => {
  afterEach(() => localStorage.clear());

  it.each([
    ["dark", webDarkTheme],
    ["light", webLightTheme],
  ] as const)("gives the document the Portal's %s paper and color scheme", (mode, base) => {
    localStorage.setItem("cetirun-theme", mode);
    const { unmount } = render(<ThemeProvider><span>content</span></ThemeProvider>);
    expect(document.documentElement.style.colorScheme).toBe(mode);
    expect(document.body).toHaveStyle({ backgroundColor: portalTheme(base).colorNeutralBackground2 });
    unmount();
    expect(document.documentElement.style.colorScheme).toBe("");
    expect(document.body.style.backgroundColor).toBe("");
  });
});
