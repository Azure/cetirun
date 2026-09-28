// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Badge, Button, Spinner, Text, tokens } from "@fluentui/react-components";
import { Warning20Regular } from "@fluentui/react-icons";
import { useCallback, useEffect, useRef, useState } from "react";
import type { Instance } from "../types/instance";
import { useAgentAuthorization } from "../hooks/useAgentAuthorization";

export function AgentAuthorizationBlock({ instance }: { instance: Instance }) {
  const enabled = instance.agentIdentity?.enabled === true;
  const authorization = useAgentAuthorization(instance.instanceName, enabled);
  const [popupError, setPopupError] = useState("");
  const popupRef = useRef<Window | null>(null);
  const popupPollRef = useRef<number | undefined>(undefined);
  const popupGraceRef = useRef<number | undefined>(undefined);

  const stopPopupWatch = useCallback(() => {
    if (popupPollRef.current !== undefined) {
      window.clearInterval(popupPollRef.current);
      popupPollRef.current = undefined;
    }
    if (popupGraceRef.current !== undefined) {
      window.clearTimeout(popupGraceRef.current);
      popupGraceRef.current = undefined;
    }
  }, []);

  const closePendingPopup = useCallback(() => {
    stopPopupWatch();
    const popup = popupRef.current;
    popupRef.current = null;
    if (popup && !popup.closed) popup.close();
  }, [stopPopupWatch]);

  const refetchStatus = authorization.status.refetch;

  useEffect(() => {
    if (!enabled) {
      stopPopupWatch();
      popupRef.current = null;
      return;
    }
    const onMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin ||
        event.source !== (popupRef.current as MessageEventSource | null) ||
        event.data?.type !== "cetirun-agent-authorization") return;
      stopPopupWatch();
      popupRef.current = null;
      if (event.data.status === "complete") {
        setPopupError("");
      } else if (event.data.status === "owner_mismatch") {
        setPopupError("Sign in with the same Microsoft Entra account used by the Portal, then try again.");
      } else {
        setPopupError("Microsoft Entra connection could not be completed. Try again.");
      }
      void refetchStatus();
    };
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      stopPopupWatch();
    };
  }, [enabled, refetchStatus, stopPopupWatch]);

  if (!enabled) return null;
  const state = authorization.status.data?.status ?? "disconnected";
  const connected = state === "connected";
  const connecting = state === "connecting" && popupError === "";

  const connect = () => {
    setPopupError("");
    closePendingPopup();
    // Open synchronously from the click gesture; waiting for the API first is
    // commonly blocked by browser popup protection.
    const popup = window.open("about:blank", "cetirun-agent-authorization", "popup,width=620,height=720");
    if (!popup) {
      setPopupError("Allow popups for this Portal, then try again");
      return;
    }
    popupRef.current = popup;
    popupPollRef.current = window.setInterval(() => {
      if (!popup.closed) return;
      if (popupPollRef.current !== undefined) {
        window.clearInterval(popupPollRef.current);
        popupPollRef.current = undefined;
      }
      // The callback posts a completion message immediately before its
      // window closes. Leave a brief grace period for that message.
      popupGraceRef.current = window.setTimeout(() => {
        if (popupRef.current !== popup) return;
        popupRef.current = null;
        popupGraceRef.current = undefined;
        setPopupError("Microsoft Entra sign-in window was closed before the connection completed. Try again.");
        void refetchStatus();
      }, 1_000);
    }, 500);
    void authorization.start.mutateAsync().then((result) => {
      if (popupRef.current !== popup || popup.closed) return;
      popup.location.replace(result.authorizationUrl);
    }).catch(() => {
      if (popupRef.current !== popup) return;
      stopPopupWatch();
      popupRef.current = null;
      popup.close();
      setPopupError("Microsoft Entra connection could not be started. Try again.");
    });
  };

  return (
    <div style={{ marginTop: 8, marginBottom: 8, padding: 12, borderRadius: tokens.borderRadiusMedium, border: `1px solid ${tokens.colorNeutralStroke1}` }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
        <Text weight="semibold">Microsoft Entra Agent Identity</Text>
        {authorization.status.isLoading || connecting ? <Badge appearance="filled" color="informative">Connecting</Badge> : connected ? <Badge appearance="filled" color="success">Connected</Badge> : <Badge appearance="filled" color="warning">Not connected</Badge>}
      </div>
      {instance.agentIdentity?.phase !== "Ready" && (
        <Text size={200}>Agent Identity is {instance.agentIdentity?.phase?.toLowerCase() || "provisioning"}. Connection will be available when it is ready.</Text>
      )}
      {authorization.status.data?.account && <Text size={200} style={{ display: "block", color: tokens.colorNeutralForeground3 }}>Connected account: {authorization.status.data.account}</Text>}
      {(popupError || authorization.status.isError || authorization.start.isError || authorization.end.isError) && (
        <div style={{ display: "flex", gap: 8, alignItems: "center", color: tokens.colorStatusDangerForeground1 }}><Warning20Regular /><Text size={200}>{popupError || authorization.status.error?.message || authorization.start.error?.message || authorization.end.error?.message}</Text></div>
      )}
      <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
        <Button appearance={connected ? "secondary" : "primary"} disabled={instance.agentIdentity?.phase !== "Ready" || authorization.start.isPending} onClick={connect}>
          {authorization.start.isPending ? <Spinner size="tiny" /> : connected ? "Reconnect Microsoft Entra" : "Connect Microsoft Entra"}
        </Button>
        {connected && <Button appearance="secondary" disabled={authorization.end.isPending} onClick={() => authorization.end.mutate()}>End connection</Button>}
      </div>
      <Text size={200} style={{ display: "block", marginTop: 8, color: tokens.colorNeutralForeground3 }}>This connection is bound to this agent. Supported runtime tools may request short-lived Azure credentials and must keep them only in process memory.</Text>
    </div>
  );
}
