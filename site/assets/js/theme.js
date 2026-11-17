// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The theme of the page. The page head runs this script before the first
// paint. "auto" follows the system setting. The theme switch of the top bar
// stores the choice of the reader.
(() => {
  const KEY = "td-color-theme";
  const system = matchMedia("(prefers-color-scheme: dark)");

  let mode = "auto";
  try {
    const stored = localStorage.getItem(KEY);
    if (stored === "light" || stored === "dark") mode = stored;
  } catch {
    // Without storage, the choice lasts for this page only.
  }

  const apply = () => {
    const theme = mode === "auto" ? (system.matches ? "dark" : "light") : mode;
    document.documentElement.setAttribute("data-bs-theme", theme);
  };
  apply();
  system.addEventListener("change", apply);

  document.addEventListener("DOMContentLoaded", () => {
    for (const option of document.querySelectorAll('input[name="cr-theme"]')) {
      option.checked = option.value === mode;
      option.addEventListener("change", () => {
        mode = option.value;
        try {
          localStorage.setItem(KEY, mode);
        } catch {
          // Keep the choice for this page.
        }
        apply();
      });
    }
  });
})();
