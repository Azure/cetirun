// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useParams, useNavigate, Navigate } from "react-router-dom";
import { Button, Text, Badge, tokens } from "@fluentui/react-components";
import {
  ArrowLeft20Regular,
  Home20Regular,
  Chat20Regular,
  WindowConsole20Regular,
  Desktop20Regular,
} from "@fluentui/react-icons";
import { useInstanceByNameQuery } from "../hooks/useInstance";
import { OverviewSection } from "../components/OverviewSection";
import { agentUIUrl, instanceGatewayUrl, phaseBadgeColor } from "../utils/instance";

type NavItem = {
  section: string;
  label: string;
  icon: React.ReactElement;
  external?: "agent" | "terminal" | "desktop";
};

const NAV_ITEMS: NavItem[] = [
  { section: "overview", label: "Overview", icon: <Home20Regular /> },
  { section: "agent-ui", label: "Agent UI", icon: <Chat20Regular />, external: "agent" },
  { section: "terminal", label: "Terminal", icon: <WindowConsole20Regular />, external: "terminal" },
  { section: "desktop", label: "Desktop", icon: <Desktop20Regular />, external: "desktop" },
  // { section: "status",   label: "Status",   icon: <Status20Regular /> },
  // { section: "settings", label: "Settings", icon: <Settings20Regular />, bottom: true },
];

const VALID_SECTIONS = ["overview"] as const;

export function WorkspacePage() {
  const { instanceName, section } = useParams<{ instanceName: string; section?: string }>();
  const navigate = useNavigate();
  const { data: instance, isLoading, isError } = useInstanceByNameQuery(instanceName);

  // Default section is overview; redirect unknown sections
  if (!section || !VALID_SECTIONS.includes(section as typeof VALID_SECTIONS[number])) {
    return <Navigate to={`/instances/${instanceName}/overview`} replace />;
  }

  const gatewayUrl = instance ? instanceGatewayUrl(instance) : undefined;
  const agentUrl = instance ? agentUIUrl(instance) : undefined;

  return (
    <div style={{ display: "flex", height: "100%", overflow: "hidden" }}>
      {/* Sidebar */}
      <div
        style={{
          width: 180,
          flexShrink: 0,
          display: "flex",
          flexDirection: "column",
          backgroundColor: tokens.colorNeutralBackground2,
          borderRight: `1px solid ${tokens.colorNeutralStroke1}`,
        }}
      >
        {/* Back link */}
        <div style={{ padding: "12px 8px", borderBottom: `1px solid ${tokens.colorNeutralStroke1}` }}>
          <Button
            appearance="subtle"
            icon={<ArrowLeft20Regular />}
            onClick={() => navigate("/")}
            style={{ width: "100%", justifyContent: "flex-start" }}
          >
            All Agents
          </Button>
        </div>

        {/* Instance name + phase */}
        <div style={{ padding: "12px 16px", borderBottom: `1px solid ${tokens.colorNeutralStroke1}` }}>
          <Text weight="semibold" size={300}>{instanceName}</Text>
          {instance && (
            <div style={{ marginTop: 4 }}>
              <Badge appearance="filled" color={phaseBadgeColor[instance.phase] ?? "informative"} size="small">
                {instance.phase}
              </Badge>
            </div>
          )}
        </div>

        {/* Nav items */}
        <div style={{ flex: 1, padding: "8px 0" }}>
          {NAV_ITEMS.map((item) => {
            const externalUrl = item.external === "agent"
              ? agentUrl
              : item.external === "terminal"
                ? gatewayUrl && `${gatewayUrl}/term`
                : item.external === "desktop"
                  ? gatewayUrl && `${gatewayUrl}/desktop`
                  : undefined;
            const externalDisabled = Boolean(item.external) && (instance?.phase !== "Ready" || !externalUrl);

            return (
              <Button
                key={item.section}
                as={item.external ? "a" : undefined}
                href={item.external ? externalUrl : undefined}
                disabled={externalDisabled}
                appearance={section === item.section ? "subtle" : "transparent"}
                icon={item.icon}
                onClick={item.external ? undefined : () => navigate(`/instances/${instanceName}/${item.section}`)}
                style={{
                  width: "100%",
                  justifyContent: "flex-start",
                  borderRadius: 0,
                  fontWeight: section === item.section ? 600 : 400,
                  borderLeft: section === item.section ? `3px solid ${tokens.colorBrandForeground1}` : "3px solid transparent",
                }}
              >
                {item.label}
              </Button>
            );
          })}
        </div>
      </div>

      {/* Content area */}
      <div style={{ flex: 1, overflow: "hidden", display: "flex", flexDirection: "column" }}>
        {isLoading && (
          <div style={{ padding: 24 }}>
            <Text>Loading...</Text>
          </div>
        )}

        {!isLoading && isError && (
          <div style={{ padding: 24 }}>
            <Text style={{ color: tokens.colorStatusDangerForeground1 }}>Failed to load agent. Please refresh the page.</Text>
          </div>
        )}

        {!isLoading && !isError && !instance && (
          <div style={{ padding: 24 }}>
            <Text style={{ color: tokens.colorStatusDangerForeground1 }}>Agent not found.</Text>
          </div>
        )}

        {instance && section === "overview" && <OverviewSection instance={instance} />}

      </div>
    </div>
  );
}
