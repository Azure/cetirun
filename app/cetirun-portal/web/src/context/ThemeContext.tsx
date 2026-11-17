// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
  createContext,
  useContext,
  useState,
  useEffect,
  type ReactNode,
} from "react";
import {
  FluentProvider,
  webLightTheme,
  webDarkTheme,
  type Theme,
} from "@fluentui/react-components";
import { portalTheme } from "../theme/portalTheme";

export type ThemeMode = "system" | "light" | "dark";

const STORAGE_KEY = "cetirun-theme";

function getStoredTheme(): ThemeMode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === "light" || stored === "dark" || stored === "system") {
      return stored;
    }
  } catch {
    // ignore storage errors (e.g. private browsing restrictions)
  }
  return "system";
}

export function resolveTheme(mode: ThemeMode, prefersDark: boolean): Theme {
  if (mode === "dark" || (mode === "system" && prefersDark)) {
    return webDarkTheme;
  }
  return webLightTheme;
}

interface ThemeContextValue {
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined);

function getSystemPrefersDark(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches
  );
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themeMode, setThemeModeState] = useState<ThemeMode>(getStoredTheme);
  const [prefersDark, setPrefersDark] = useState(getSystemPrefersDark);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const handler = (e: MediaQueryListEvent) => setPrefersDark(e.matches);
    // Prefer the modern addEventListener API; fall back to the legacy addListener
    // for browsers that don't yet support it on MediaQueryList.
    if (typeof mq.addEventListener === "function") {
      mq.addEventListener("change", handler);
      return () => mq.removeEventListener("change", handler);
    } else {
      // eslint-disable-next-line @typescript-eslint/no-deprecated
      mq.addListener(handler);
      // eslint-disable-next-line @typescript-eslint/no-deprecated
      return () => mq.removeListener(handler);
    }
  }, []);

  const setThemeMode = (mode: ThemeMode) => {
    setThemeModeState(mode);
    try {
      localStorage.setItem(STORAGE_KEY, mode);
    } catch {
      // ignore storage errors
    }
  };

  const base = resolveTheme(themeMode, prefersDark);
  const theme = portalTheme(base);

  // Keep the document in sync so the page outside the React root, native
  // controls and scrollbars also follow the active theme (avoids a white
  // flash / white margin area).
  useEffect(() => {
    const root = document.documentElement;
    root.style.colorScheme = base === webDarkTheme ? "dark" : "light";
    document.body.style.backgroundColor = theme.colorNeutralBackground2;
    document.body.style.color = theme.colorNeutralForeground1;
    return () => {
      root.style.colorScheme = "";
      document.body.style.backgroundColor = "";
      document.body.style.color = "";
    };
  }, [base, theme]);

  return (
    <ThemeContext.Provider value={{ themeMode, setThemeMode }}>
      <FluentProvider theme={theme}>{children}</FluentProvider>
    </ThemeContext.Provider>
  );
}

export function useThemeContext(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error("useThemeContext must be used within a ThemeProvider");
  }
  return ctx;
}
