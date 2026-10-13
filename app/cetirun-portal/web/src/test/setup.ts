// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import "@testing-library/jest-dom/vitest";
import { configure } from "@testing-library/dom";

// Full-suite workers can spend more than the one-second default initializing
// Fluent UI and IndexedDB-backed views on constrained CI runners.
configure({ asyncUtilTimeout: 5_000 });

// Polyfill ResizeObserver for FluentUI components in jsdom
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// jsdom doesn't implement scrollIntoView, which the chat view calls on new messages.
Element.prototype.scrollIntoView = () => {};
