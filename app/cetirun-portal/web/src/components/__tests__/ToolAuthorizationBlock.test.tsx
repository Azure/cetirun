// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { type Connection, type ConnectionBinding } from "../../api/toolAuthorization";
import type { Instance } from "../../types/instance";
import { ToolAuthorizationBlock } from "../ToolAuthorizationBlock";
import MyAccountsDialog from "../MyAccountsDialog";

const api = vi.hoisted(() => ({ listConnections: vi.fn(), listConnectionBindings: vi.fn(), startConnection: vi.fn(), reconnectConnection: vi.fn(), deleteConnection: vi.fn(), getConnectionUsage: vi.fn(), createToolIdentity: vi.fn(), createConnectionBinding: vi.fn(), updateConnectionBinding: vi.fn(), deleteConnectionBinding: vi.fn() }));
vi.mock("../../api/toolAuthorization", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../api/toolAuthorization")>()), ...api }));
const instance: Instance = { instanceName: "agent-a", uid: "agent-uid", resourceVersion: "1", agent: { type: "hermes" }, alias: "owner", displayName: "Agent A", team: "platform", phase: "Ready", createdAt: "2026-10-10T00:00:00Z", upgradeAvailable: false };
const github: Connection = { id: "connection-github", provider: "github", accountDisplay: "octocat", services: ["github"], scopes: ["repo"], mode: "delegated", revision: 3, status: "connected", createdAt: "2026-10-10T00:00:00Z" };
const binding: ConnectionBinding = { id: "binding-1", instanceUid: instance.uid, connectionId: github.id, services: ["github"], enabled: true, revision: 7 };
function renderUI(content = <ToolAuthorizationBlock instance={instance} />) { const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); return render(<QueryClientProvider client={client}>{content}</QueryClientProvider>); }

describe("Agent connection lifecycle", () => {
 beforeEach(() => {
  Object.values(api).forEach((mock) => mock.mockReset());
  api.listConnections.mockResolvedValue([github]); api.listConnectionBindings.mockResolvedValue({ bindings: [], readOnly: false });
  api.createConnectionBinding.mockResolvedValue({}); api.updateConnectionBinding.mockResolvedValue({}); api.deleteConnectionBinding.mockResolvedValue(undefined);
  api.getConnectionUsage.mockResolvedValue({ connectionId: github.id, agents: [], canRemove: true });
 });
 it("shows only associated connections and requires confirmation in Connect", async () => {
  const user = userEvent.setup(); renderUI();
  await screen.findByText("No connections for this agent.");
  expect(screen.queryByText(/octocat/)).toBeNull(); expect(api.createConnectionBinding).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Connect" }));
  await user.click(screen.getByRole("button", { name: "GitHub — octocat" }));
  const dialog = within(screen.getByRole("dialog"));
  expect(dialog.getByRole("checkbox", { name: "GitHub REST API" })).toBeChecked();
  expect(api.createConnectionBinding).not.toHaveBeenCalled();
  await user.click(dialog.getByRole("button", { name: "Connect" }));
  await waitFor(() => expect(api.createConnectionBinding).toHaveBeenCalledWith("agent-a", { connectionId: github.id, services: ["github"], enabled: true }));
 });
 it("Disconnect uses the current Agent revision and never removes the account", async () => {
  api.listConnectionBindings.mockResolvedValue({ bindings: [binding], readOnly: false });
  const user = userEvent.setup(); renderUI();
  await user.click(await screen.findByRole("button", { name: "Disconnect" }));
  await waitFor(() => expect(api.deleteConnectionBinding).toHaveBeenCalledWith("agent-a", "binding-1", 7));
  expect(api.deleteConnection).not.toHaveBeenCalled();
  for (const name of ["Refresh", "Reconnect", "Reauthorize", "Use for this agent", "Disable for this agent", "Edit services"]) expect(screen.queryByRole("button", { name })).toBeNull();
 });
 it("allows Disconnect for unavailable connections and read-only runtimes", async () => {
  api.listConnections.mockResolvedValue([]); api.listConnectionBindings.mockResolvedValue({ bindings: [binding], readOnly: true });
  const user = userEvent.setup(); renderUI();
  await user.click(await screen.findByRole("button", { name: "Disconnect" }));
  expect(api.deleteConnectionBinding).toHaveBeenCalledWith("agent-a", "binding-1", 7);
  expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
 });
 it("reuses this Agent's Azure identity without a second resource grant", async () => {
  const azure: Connection = { ...github, id: "azure", mode: "agent", provider: "azure", accountDisplay: "Agent A", services: ["azure_arm"], scopes: [], identity: { instanceUid: instance.uid, tenantId: "tenant", principalId: "principal", clientId: "client" } };
  api.listConnections.mockResolvedValue([azure, { ...azure, id: "other", identity: { ...azure.identity!, instanceUid: "other-agent" } }]);
  const user = userEvent.setup(); renderUI(); await screen.findByText("No connections for this agent.");
  await user.click(screen.getByRole("button", { name: "Connect" })); await user.click(screen.getByRole("button", { name: "Azure Agent identity" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Connect" }));
  expect(api.createToolIdentity).not.toHaveBeenCalled(); expect(api.createConnectionBinding).toHaveBeenCalledWith("agent-a", expect.objectContaining({ connectionId: "azure" }));
 });
 it("requires at least one service and shows Edit services only for multiple services", async () => {
  const google: Connection = { ...github, provider: "google", services: ["google_gmail", "google_calendar"] };
  api.listConnections.mockResolvedValue([google]); api.listConnectionBindings.mockResolvedValue({ bindings: [{ ...binding, services: google.services }], readOnly: false });
  const user = userEvent.setup(); renderUI(); await user.click(await screen.findByRole("button", { name: "Edit services" }));
  await user.click(screen.getByRole("checkbox", { name: "Gmail" })); await user.click(screen.getByRole("checkbox", { name: "Google Calendar" }));
  expect(screen.getByRole("button", { name: "Save services" })).toBeDisabled();
 });
 it("does not restore disabled records or bind when cancelled", async () => {
  api.listConnectionBindings.mockResolvedValue({ bindings: [{ ...binding, enabled: false }], readOnly: false });
  const user = userEvent.setup(); renderUI(); await screen.findByText("No connections for this agent.");
  await user.click(screen.getByRole("button", { name: "Connect" })); await user.click(screen.getByRole("button", { name: "GitHub — octocat" })); await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(api.createConnectionBinding).not.toHaveBeenCalled(); expect(api.updateConnectionBinding).not.toHaveBeenCalled();
 });
 it("offers only the canonical account when old duplicate connections exist", async () => {
  api.listConnections.mockResolvedValue([{ ...github, canonicalConnectionId: github.id }, { ...github, id: "duplicate", canonicalConnectionId: github.id }]);
  const user = userEvent.setup(); renderUI(); await screen.findByText("No connections for this agent."); await user.click(screen.getByRole("button", { name: "Connect" }));
  expect(screen.getAllByRole("button", { name: "GitHub — octocat" })).toHaveLength(1);
 });
 it("fails closed on load errors and offers Retry", async () => {
  api.listConnectionBindings.mockRejectedValue(new Error("Unavailable")); renderUI(); await screen.findByRole("alert");
  expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled(); expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
 });
 it("does not start OAuth when the popup is blocked", async () => {
  vi.spyOn(window, "open").mockReturnValue(null);
  const user = userEvent.setup(); renderUI(); await screen.findByText("No connections for this agent."); await user.click(screen.getByRole("button", { name: "Connect" })); await user.click(screen.getByRole("button", { name: "Sign in" }));
  expect(screen.getByText(/Allow popups/)).toBeInTheDocument(); expect(api.startConnection).not.toHaveBeenCalled(); vi.restoreAllMocks();
 });
 it("My accounts blocks removal while any Agent uses an account", async () => {
  api.getConnectionUsage.mockResolvedValue({ connectionId: github.id, agents: [{ instanceUid: "b", instanceName: "agent-b", instanceNamespace: "ns-b" }], canRemove: false });
  renderUI(<MyAccountsDialog onClose={() => {}} />); await screen.findByText(/Used by: agent-b/);
  expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled(); expect(api.deleteConnection).not.toHaveBeenCalled();
 });
 it("My accounts removes only unused accounts after explicit confirmation", async () => {
  api.deleteConnection.mockResolvedValue(undefined);
  const user = userEvent.setup(); renderUI(<MyAccountsDialog onClose={() => {}} />); await screen.findByText("No agents use this account.");
  await user.click(screen.getByRole("button", { name: "Remove" })); expect(api.deleteConnection).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Remove account" })); await waitFor(() => expect(api.deleteConnection).toHaveBeenCalledWith(github.id));
 });
 it("isolates Binding cache and workflow state when the same Agent name gets a new UID", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  api.listConnectionBindings.mockResolvedValueOnce({ bindings: [binding], readOnly: false });
  const user = userEvent.setup();
  const view = render(<QueryClientProvider client={client}><ToolAuthorizationBlock instance={instance} /></QueryClientProvider>);
  await screen.findByText("GitHub — octocat");
  await user.click(screen.getByRole("button", { name: "Connect" }));
  expect(screen.getByRole("dialog")).toBeInTheDocument();
  let finish!: (value: { bindings: ConnectionBinding[]; readOnly: boolean }) => void;
  api.listConnectionBindings.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  view.rerender(<QueryClientProvider client={client}><ToolAuthorizationBlock instance={{ ...instance, uid: "new-agent-uid" }} /></QueryClientProvider>);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  await waitFor(() => expect(api.listConnectionBindings).toHaveBeenCalledTimes(2));
  finish({ bindings: [], readOnly: false });
  await screen.findByText("No connections for this agent.");
  expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
 });
 it("recovers from a Binding load failure through Retry", async () => {
  api.listConnectionBindings.mockRejectedValueOnce(new Error("Unavailable"));
  const user = userEvent.setup(); renderUI(); await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByText("No connections for this agent.");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
 });
 it("refetches a conflicted Disconnect and retries with the refreshed revision", async () => {
  api.listConnectionBindings.mockResolvedValue({ bindings: [{ ...binding, revision: 8 }], readOnly: false }).mockResolvedValueOnce({ bindings: [binding], readOnly: false });
  api.deleteConnectionBinding.mockRejectedValueOnce(new Error("Connection changed"));
  const user = userEvent.setup(); renderUI();
  await user.click(await screen.findByRole("button", { name: "Disconnect" }));
  await screen.findByText("Connection changed");
  await waitFor(() => expect(api.listConnectionBindings).toHaveBeenCalledTimes(2));
  await user.click(screen.getByRole("button", { name: "Disconnect" }));
  await waitFor(() => expect(api.deleteConnectionBinding).toHaveBeenLastCalledWith("agent-a", "binding-1", 8));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
 });
 it("uses a fresh Binding revision when retrying an Edit services conflict", async () => {
  const google: Connection = { ...github, provider: "google", services: ["google_gmail", "google_calendar"] };
  const current = { ...binding, services: google.services };
  api.listConnections.mockResolvedValue([google]);
  api.listConnectionBindings.mockResolvedValue({ bindings: [current], readOnly: false });
  const user = userEvent.setup(); renderUI();
  await user.click(await screen.findByRole("button", { name: "Edit services" }));
  api.listConnectionBindings.mockResolvedValue({ bindings: [{ ...current, revision: 9 }], readOnly: false });
  api.updateConnectionBinding.mockRejectedValueOnce(new Error("Services changed"));
  await user.click(screen.getByRole("checkbox", { name: "Gmail" }));
  await user.click(screen.getByRole("button", { name: "Save services" }));
  await screen.findByText("Services changed");
  await user.click(screen.getByRole("button", { name: "Save services" }));
  await waitFor(() => expect(api.updateConnectionBinding).toHaveBeenLastCalledWith("agent-a", "binding-1", { connectionId: github.id, services: ["google_calendar"], enabled: true, revision: 9 }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
 });

});
