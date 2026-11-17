// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InstanceListPage } from "./InstanceListPage";

const instances = vi.hoisted(() => ({
  data: [
    { instanceName: "research", displayName: "Research", agent: { type: "openclaw" }, phase: "Failed" },
    { instanceName: "support", displayName: "Support", agent: { type: "hermes" }, phase: "Ready" },
  ] as unknown[] | undefined,
}));
const features = vi.hoisted(() => ({ data: { maxInstancesPerUser: 3 } as { maxInstancesPerUser: number } | undefined }));
vi.mock("../hooks/useInstance", () => ({ useInstancesQuery: () => instances }));
vi.mock("../hooks/useFeatures", () => ({ useFeatures: () => features }));
vi.mock("../components/CreateForm", () => ({ CreateForm: () => null }));

const renderPage = () => render(<MemoryRouter><InstanceListPage /></MemoryRouter>);

describe("InstanceListPage rows", () => {
  const two = instances.data;
  beforeEach(() => {
    features.data = { maxInstancesPerUser: 3 };
    instances.data = two;
  });

  it("opens an Agent's overview from its name", () => {
    renderPage();
    expect(screen.getByRole("link", { name: "research" })).toHaveAttribute("href", "/instances/research/overview");
    expect(screen.getByRole("link", { name: "support" })).toHaveAttribute("href", "/instances/support/overview");
  });

  it("draws each Agent's phase next to its name", () => {
    renderPage();
    const failed = screen.getByRole("group", { name: "research" });
    expect(within(failed).getByText("Failed")).toBeInTheDocument();
    expect(failed.querySelector("svg.lifecycle")).toHaveClass("lifecycle--failed");
    expect(screen.getByRole("group", { name: "support" }).querySelector("svg.lifecycle")).toHaveClass("lifecycle--ready");
  });

  it("counts the Agents against the limit", () => {
    renderPage();
    expect(screen.getByText("2 of 3 agents")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New Agent" })).toBeEnabled();
  });

  it("counts the Agents alone when there is no limit", () => {
    features.data = { maxInstancesPerUser: 0 };
    renderPage();
    expect(screen.getByText("2 agents")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New Agent" })).toBeEnabled();
  });

  it("counts one Agent in the singular when there is no limit", () => {
    features.data = { maxInstancesPerUser: 0 };
    instances.data = two!.slice(0, 1);
    renderPage();
    expect(screen.getByText("1 agent")).toBeInTheDocument();
  });

  it("does not count before the limit is known", () => {
    features.data = undefined;
    renderPage();
    expect(screen.queryByText(/\d+ (of \d+ )?agents$/)).toBeNull();
  });

  it("stops new Agents at the limit", () => {
    features.data = { maxInstancesPerUser: 2 };
    renderPage();
    expect(screen.getByText("2 of 2 agents")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New Agent" })).toBeDisabled();
  });
});
