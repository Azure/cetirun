import { useEffect, useMemo, useState } from "react";
import {
  Badge,
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Spinner,
  Text,
  tokens,
} from "@fluentui/react-components";
import {
  Checkmark20Regular,
  Copy20Regular,
  Open20Regular,
  Warning20Regular,
} from "@fluentui/react-icons";
import {
  isCopilotAuthSupported,
  useCopilotAuthStatusQuery,
  useCopilotLogin,
} from "../hooks/useCopilotAuth";
import type { Instance } from "../types/instance";

function formatRelative(timestampSeconds: number): string {
  if (!timestampSeconds) return "never";
  const deltaSeconds = Math.max(0, Math.floor(Date.now() / 1000) - timestampSeconds);
  if (deltaSeconds < 5) return "just now";
  if (deltaSeconds < 60) return `${deltaSeconds}s ago`;
  if (deltaSeconds < 3600) return `${Math.floor(deltaSeconds / 60)} min ago`;
  if (deltaSeconds < 86400) return `${Math.floor(deltaSeconds / 3600)}h ago`;
  return `${Math.floor(deltaSeconds / 86400)}d ago`;
}

export function CopilotAuthBlock({ instance }: { instance: Instance }) {
  const supported = isCopilotAuthSupported(instance);
  const statusQuery = useCopilotAuthStatusQuery(instance);
  const login = useCopilotLogin(instance);
  const [copied, setCopied] = useState(false);
  const [, setNow] = useState(0);

  // Re-render every minute so the "Last checked: 2 min ago" stays fresh.
  useEffect(() => {
    const id = setInterval(() => setNow((n) => n + 1), 60_000);
    return () => clearInterval(id);
  }, []);

  const dialogOpen =
    login.state.phase === "starting" ||
    login.state.phase === "awaiting_user" ||
    login.state.phase === "succeeded" ||
    login.state.phase === "expired" ||
    login.state.phase === "error";

  const status = statusQuery.data;
  const authenticated = status?.authenticated === true;

  const lastChecked = useMemo(
    () => (status ? formatRelative(status.lastCheckedAt) : "—"),
    // Recompute when lastCheckedAt changes; the interval above forces re-render
    // so the relative label updates over time.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [status?.lastCheckedAt, status?.authenticated, status?.model],
  );

  if (!supported) return null;

  const handleCopy = () => {
    if (!login.state.start) return;
    navigator.clipboard
      .writeText(login.state.start.userCode)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      })
      .catch((err) => {
        // Insecure context, denied permission, or no clipboard API — keep the
        // UI quiet and let the user copy the code manually from the dialog.
        console.warn("clipboard write failed", err);
      });
  };

  const handleStart = () => {
    setCopied(false);
    void login.startLogin();
  };

  const handleClose = () => {
    if (login.state.phase === "awaiting_user" || login.state.phase === "starting") {
      void login.cancel();
    } else {
      login.reset();
    }
  };

  return (
    <div
      style={{
        marginTop: 8,
        marginBottom: 8,
        padding: 12,
        borderRadius: tokens.borderRadiusMedium,
        border: `1px solid ${tokens.colorNeutralStroke1}`,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
        <Text weight="semibold">Copilot Auth</Text>
        {statusQuery.isLoading ? (
          <Badge appearance="filled" color="informative">Checking...</Badge>
        ) : authenticated ? (
          <Badge appearance="filled" color="success">Authenticated</Badge>
        ) : (
          <Badge appearance="filled" color="warning">Not authenticated</Badge>
        )}
      </div>

      {statusQuery.isError && (
        <div style={{ display: "flex", alignItems: "flex-start", gap: 8, marginBottom: 8 }}>
          <Warning20Regular style={{ color: tokens.colorStatusDangerForeground1, flexShrink: 0, marginTop: 2 }} />
          <Text size={200} style={{ color: tokens.colorStatusDangerForeground1 }}>
            {(statusQuery.error as Error).message || "Failed to load auth status."}
          </Text>
        </div>
      )}

      {status && (
        <div style={{ display: "flex", flexDirection: "column", gap: 4, marginBottom: 8 }}>
          <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>
            Provider: <code style={{ fontFamily: "monospace", fontSize: 12 }}>{status.provider}</code>
            {status.model ? (
              <>
                {" "}· Model: <code style={{ fontFamily: "monospace", fontSize: 12 }}>{status.model}</code>
              </>
            ) : null}
          </Text>
          <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>
            Last checked: {lastChecked}
          </Text>
        </div>
      )}

      <div style={{ display: "flex", gap: 8 }}>
        <Button
          appearance={authenticated ? "secondary" : "primary"}
          onClick={handleStart}
          disabled={login.state.phase === "starting" || login.state.phase === "awaiting_user"}
        >
          {authenticated ? "Re-authenticate" : "Login with GitHub"}
        </Button>
      </div>

      <Dialog open={dialogOpen} onOpenChange={(_, data) => { if (!data.open) handleClose(); }}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>GitHub Copilot Login</DialogTitle>
            <DialogContent>
              {login.state.phase === "starting" && (
                <Spinner size="small" label="Requesting device code..." />
              )}

              {login.state.phase === "awaiting_user" && login.state.start && (
                <div style={{ display: "flex", flexDirection: "column", gap: 12, paddingTop: 8 }}>
                  <Text>
                    Open the GitHub device authorization page and enter the code below:
                  </Text>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <Text style={{ fontFamily: "monospace", fontSize: 18, letterSpacing: 2 }}>
                      {login.state.start.userCode}
                    </Text>
                    <Button
                      appearance="subtle"
                      size="small"
                      icon={copied ? <Checkmark20Regular /> : <Copy20Regular />}
                      onClick={handleCopy}
                      aria-label="Copy code"
                    />
                  </div>
                  <div>
                    <Button
                      appearance="primary"
                      icon={<Open20Regular />}
                      onClick={() => window.open(login.state.start!.verificationUri, "_blank", "noopener,noreferrer")}
                    >
                      Open GitHub
                    </Button>
                  </div>
                  <Spinner size="tiny" label="Waiting for authorization..." />
                </div>
              )}

              {login.state.phase === "succeeded" && (
                <div style={{ display: "flex", flexDirection: "column", gap: 8, paddingTop: 8 }}>
                  <Text>Authenticated. The gateway is restarting to pick up the new token.</Text>
                </div>
              )}

              {login.state.phase === "expired" && (
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8, paddingTop: 8 }}>
                  <Warning20Regular style={{ color: tokens.colorStatusWarningForeground1, flexShrink: 0, marginTop: 2 }} />
                  <Text>{login.state.errorMessage || "Device code expired. Please try again."}</Text>
                </div>
              )}

              {login.state.phase === "error" && (
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8, paddingTop: 8 }}>
                  <Warning20Regular style={{ color: tokens.colorStatusDangerForeground1, flexShrink: 0, marginTop: 2 }} />
                  <Text style={{ color: tokens.colorStatusDangerForeground1 }}>
                    {login.state.errorMessage || "Authorization failed."}
                  </Text>
                </div>
              )}
            </DialogContent>
            <DialogActions>
              {login.state.phase === "awaiting_user" || login.state.phase === "starting" ? (
                <Button appearance="secondary" onClick={() => void login.cancel()}>Cancel</Button>
              ) : (
                <Button appearance="primary" onClick={() => login.reset()}>Close</Button>
              )}
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
