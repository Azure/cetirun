// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { SandboxEditPage } from "./SandboxEditPage";
import { SandboxListPage } from "./SandboxListPage";
import * as api from "../../api/adminSandbox";

vi.mock("../../api/adminSandbox");
const quantities = { cpu: "1", memory: "1Gi", "ephemeral-storage": "1Gi" };
const box: api.SandboxDTO = { name: "example", uid: "sandbox-uid", resourceVersion: "7", type: "kata", ready: "Unknown", deleting: false,
  resources: { agent: { requests: quantities, limits: quantities }, helpers: { requests: quantities, limits: quantities } },
  storage: { home: { storageClassName: "example-csi", size: "10Gi", accessMode: "ReadWriteOncePod" }, scratch: { sizeLimit: "1Gi" } },
};
beforeEach(() => {
  vi.resetAllMocks(); vi.mocked(api.getSandbox).mockResolvedValue(box); vi.mocked(api.listSandboxes).mockResolvedValue([box]);
  vi.mocked(api.createSandbox).mockResolvedValue(box); vi.mocked(api.updateSandbox).mockResolvedValue(box);
});
function renderPage(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes>
    <Route path="/admin/sandboxes/new" element={<SandboxEditPage />} />
    <Route path="/admin/sandboxes/:name/edit" element={<SandboxEditPage />} />
    <Route path="/admin/sandboxes" element={<SandboxListPage />} />
  </Routes></MemoryRouter></QueryClientProvider>);
  return client;
}

it.each([
  { name: "default", helperMemoryLimit: "2Gi" },
  { name: "custom", helperMemoryLimit: "3Gi" },
])("creates complete Sandbox resources with $name helper memory without claiming readiness", async ({ helperMemoryLimit }) => {
  const user = userEvent.setup(); renderPage("/admin/sandboxes/new");
  const helperMemory = screen.getByRole("textbox", { name: "Helper memory limit" });
  expect(helperMemory).toHaveValue("2Gi");
  if (helperMemoryLimit !== "2Gi") {
    await user.clear(helperMemory);
    await user.type(helperMemory, helperMemoryLimit);
  }
  await user.type(screen.getByRole("textbox", { name: "Name" }), "example");
  await user.selectOptions(screen.getByRole("combobox", { name: "Runtime" }), "gvisor");
  await user.type(screen.getByRole("textbox", { name: "HOME StorageClass" }), "example-csi");
  await user.click(screen.getByRole("button", { name: "Create" }));
  await waitFor(() => expect(api.createSandbox).toHaveBeenCalled());
  const request = vi.mocked(api.createSandbox).mock.calls[0][0];
  expect(request).toMatchObject({ name: "example", type: "gvisor", storage: box.storage });
  expect(request.resources.agent.requests).toHaveProperty("ephemeral-storage");
  expect(request.resources.helpers).toEqual({
    requests: { cpu: "100m", memory: "128Mi", "ephemeral-storage": "128Mi" },
    limits: { cpu: "500m", memory: helperMemoryLimit, "ephemeral-storage": "1Gi" },
  });
  expect(request).not.toHaveProperty("ready");
});

it("edits a Sandbox named new using its exact version and displays rejection details", async () => {
  vi.mocked(api.getSandbox).mockResolvedValue({ ...box, name: "new" });
  vi.mocked(api.updateSandbox).mockRejectedValue(new Error("Agent cpu request must not exceed its limit"));
  const user = userEvent.setup(); renderPage("/admin/sandboxes/new/edit");
  expect(await screen.findByRole("textbox", { name: "Name" })).toBeDisabled();
  await waitFor(() => expect(screen.getByRole("textbox", { name: "HOME StorageClass" })).toHaveValue("example-csi"));
  expect(screen.getByRole("textbox", { name: "Helper memory limit" })).toHaveValue("1Gi");
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText(/request must not exceed/)).toBeInTheDocument();
  expect(api.updateSandbox).toHaveBeenCalledExactlyOnceWith("new", expect.objectContaining({ uid: "sandbox-uid", resourceVersion: "7", resources: box.resources, storage: box.storage }));
  expect(vi.mocked(api.updateSandbox).mock.calls[0][1]).not.toHaveProperty("ready");
  expect(api.createSandbox).not.toHaveBeenCalled();
});

it("deletes the version selected for confirmation even if the list refreshes", async () => {
  const user = userEvent.setup(); const client = renderPage("/admin/sandboxes");
  await user.click(await screen.findByRole("button", { name: "Delete example" }));
  await act(async () => { client.setQueryData(["admin", "sandboxes"], [{ ...box, uid: "replacement-uid", resourceVersion: "20" }]); });
  await user.click(screen.getByRole("button", { name: "Delete", exact: true }));
  await waitFor(() => expect(api.deleteSandbox).toHaveBeenCalledExactlyOnceWith(box));
});
