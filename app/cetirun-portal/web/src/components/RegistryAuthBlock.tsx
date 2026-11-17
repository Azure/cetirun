// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Button, Field, Input, MessageBar } from "@fluentui/react-components";
import type { Instance } from "../types/instance";
import { updateRegistryAuth } from "../api/registry";

export function RegistryAuthBlock({ instance }: { instance: Instance }) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [target, setTarget] = useState<Instance | null>(null);
  const canManage = instance.phase !== "Deleting";

  if (!instance.agent.registrySecretRef) return null;

  const close = () => {
    setUsername("");
    setPassword("");
    setEditing(false);
    setRemoving(false);
    setTarget(null);
  };

  const submit = async (remove: boolean) => {
    setBusy(true);
    setError("");
    try {
      await updateRegistryAuth(target ?? instance, remove ? undefined : { username, password });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Unable to update registry credentials");
    } finally {
      close();
      setBusy(false);
      void queryClient.invalidateQueries({ queryKey: ["instance"] });
      void queryClient.invalidateQueries({ queryKey: ["instances"] });
    }
  };

  const image = (target ?? instance).agent.image;

  return (
    <section aria-label="Registry credentials" className="portal-section">
      <h2 className="portal-section-title">Registry credentials</h2>
      <p>{instance.registryAuthConfigured ? "Configured (write-only)" : "Not configured"}</p>
      <p className={image ? "portal-note portal-mono" : "portal-note"}>{image || "Waiting for the target image to be resolved"}</p>
      {canManage && !editing && !removing && (
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <Button disabled={!instance.agent.image} onClick={() => { setError(""); setTarget(instance); setEditing(true); }}>
            Replace registry credentials
          </Button>
          <Button onClick={() => { setTarget(instance); setRemoving(true); }}>Remove registry credentials</Button>
        </div>
      )}
      {canManage && editing && (
        <form onSubmit={(event) => { event.preventDefault(); void submit(false); }} className="portal-stack" style={{ maxWidth: 480 }}>
          <Field label="Registry username" required>
            <Input style={{ minWidth: 0, width: "100%" }} autoComplete="off" value={username} disabled={busy} onChange={(_, data) => setUsername(data.value)} required />
          </Field>
          <Field label="Registry password or token" required hint="Enter a new value. Saved credentials are never displayed.">
            <Input style={{ minWidth: 0, width: "100%" }} type="password" autoComplete="new-password" value={password} disabled={busy} onChange={(_, data) => setPassword(data.value)} required />
          </Field>
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            <Button type="submit" appearance="primary" disabled={busy || !username.trim() || !password.trim()}>{busy ? "Saving..." : "Save registry credentials"}</Button>
            <Button disabled={busy} onClick={close}>Cancel</Button>
          </div>
        </form>
      )}
      {canManage && removing && (
        <div>
          <p style={{ margin: "0 0 12px" }}>Remove the managed credentials? Future image pulls may fail without another node authentication method.</p>
          <Button disabled={busy} onClick={() => void submit(true)}>Confirm removal</Button>{" "}
          <Button disabled={busy} onClick={close}>Cancel</Button>
        </div>
      )}
      {error && <MessageBar intent="error">{error}</MessageBar>}
    </section>
  );
}
