// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { lazy, Suspense } from "react";
import { useParams, useNavigate, Navigate } from "react-router-dom";
import { Button } from "@fluentui/react-components";
import {
  ArrowLeft20Regular,
  ArrowUpRight16Regular,
  Home20Regular,
  Chat20Regular,
  WindowConsole20Regular,
  Desktop20Regular,
} from "@fluentui/react-icons";
import { useInstanceByNameQuery } from "../hooks/useInstance";
import { useMe } from "../context/MeContext";
import { SESSION_EXPIRED_MESSAGE } from "../api/fetch";
import { OverviewSection } from "../components/OverviewSection";
import { HermesWebappButton } from "../components/HermesWebappButton";
import { AgentPhase } from "../components/Lifecycle";
import { agentUIUrl, desktopUrl, terminalUrl } from "../utils/instance";
import { useFeatures } from "../hooks/useFeatures";
import { useChatAccount } from "../chat/ChatAccount";

const ChatPage = lazy(() => import("../chat/ChatPage"));

type NavItem = {
  section: string;
  label: string;
  icon: React.ReactElement;
  external?: "agent" | "terminal" | "desktop";
};

const NAV_ITEMS: NavItem[] = [
  { section: "overview", label: "Overview", icon: <Home20Regular /> },
  { section: "chat", label: "Chat", icon: <Chat20Regular /> },
  {
    section: "agent-ui",
    label: "Agent UI",
    icon: <Chat20Regular />,
    external: "agent",
  },
  {
    section: "terminal",
    label: "Terminal",
    icon: <WindowConsole20Regular />,
    external: "terminal",
  },
  {
    section: "desktop",
    label: "Desktop",
    icon: <Desktop20Regular />,
    external: "desktop",
  },
];

const VALID_SECTIONS = ["overview", "chat"] as const;

export function WorkspacePage() {
  const { instanceName, section } = useParams<{
    instanceName: string;
    section?: string;
  }>();
  const navigate = useNavigate();
  const {
    data: instance,
    isLoading: instanceLoading,
    isError: instanceError,
    error,
  } = useInstanceByNameQuery(instanceName);
  const viewer = useMe();
  const features = useFeatures();
  const chatAccount = useChatAccount();
  const chatSessionExpired = chatAccount?.error === SESSION_EXPIRED_MESSAGE;
  const isError = instanceError || !!viewer.error || chatSessionExpired;
  const isLoading = !isError && (instanceLoading || viewer.isLoading);
  const errorMessage = chatSessionExpired ? SESSION_EXPIRED_MESSAGE
    : viewer.error instanceof Error ? viewer.error.message
    : error?.message ?? "Failed to load agent. Please refresh the page.";
  const chatEligible =
    instance?.agent.type === "custom" &&
    instance.agent.port != null &&
    !instance.agent.webUI;
  const chatEnabled = features.data?.customChatEnabled === true;

  // Default section is overview; redirect unknown sections
  if (
    !section ||
    !VALID_SECTIONS.includes(section as (typeof VALID_SECTIONS)[number])
  ) {
    return <Navigate to={`/instances/${instanceName}/overview`} replace />;
  }

  const externalUrls = {
    agent: instance && !isError ? agentUIUrl(instance) : undefined,
    terminal: instance && !isError ? terminalUrl(instance) : undefined,
    desktop: instance && !isError ? desktopUrl(instance) : undefined,
  };
  const hasWebUI = instance?.agent.type === "custom" && !!instance.agent.webUI;

  if (section === "chat" && instance && !isError) {
    if (!chatEligible)
      return <Navigate to={`/instances/${instanceName}/overview`} replace />;
    if (features.isLoading)
      return <div className="portal-detail">Checking Chat availability…</div>;
    if (!chatEnabled)
      return <Navigate to={`/instances/${instanceName}/overview`} replace />;
    return (
      <Suspense fallback={<div className="portal-detail">Opening chat…</div>}>
        <ChatPage
          key={`${instance.uid}:${chatAccount?.scope ?? ""}`}
          instance={instance}
        />
      </Suspense>
    );
  }

  return (
    <div className="portal-split">
      <div className="portal-side">
        <Button
          className="portal-side-link"
          appearance="transparent"
          icon={<ArrowLeft20Regular />}
          onClick={() => navigate("/")}
        >
          All Agents
        </Button>

        <div className="portal-side-identity">
          <span className="portal-side-identity-name">
            {instance?.displayName.trim() || instanceName}
          </span>
          {instance && <AgentPhase phase={instance.phase} size="small" />}
        </div>

        <div className="portal-side-nav" role="navigation" aria-label="Agent workspace">
          {NAV_ITEMS.map((item) => {
            if (item.section === "chat" && (!chatEligible || !chatEnabled))
              return null;
            const externalUrl = item.external
              ? externalUrls[item.external]
              : undefined;
            const externalDisabled =
              Boolean(item.external) &&
              (instance?.phase !== "Ready" || !externalUrl);

            if (item.external === "agent" && instance?.agent.type === "hermes") {
              return <HermesWebappButton key={item.section} instance={instance} disabled={externalDisabled} />;
            }

            return (
              <Button
                key={item.section}
                className="portal-side-link"
                as={item.external ? "a" : undefined}
                href={item.external ? externalUrl : undefined}
                target={
                  item.external === "agent" && hasWebUI ? "_blank" : undefined
                }
                rel={
                  item.external === "agent" && hasWebUI
                    ? "noopener noreferrer"
                    : undefined
                }
                disabled={externalDisabled}
                aria-current={section === item.section ? "page" : undefined}
                appearance="transparent"
                icon={item.icon}
                onClick={
                  item.external
                    ? undefined
                    : () =>
                        navigate(`/instances/${instanceName}/${item.section}`)
                }
              >
                {item.external === "agent" && hasWebUI ? "Web UI" : item.label}
                {item.external && <ArrowUpRight16Regular className="portal-side-link-out" />}
              </Button>
            );
          })}
        </div>
      </div>

      <div className="portal-split-main">
        {isLoading && <div className="portal-detail">Loading...</div>}

        {!isLoading && isError && (
          <div role="alert" className="portal-detail">
            <span className="portal-error-text">{errorMessage}</span>
          </div>
        )}

        {!isLoading && !isError && !instance && (
          <div className="portal-detail">
            <span className="portal-error-text">Agent not found.</span>
          </div>
        )}

        {instance && !isError && section === "overview" && (
          <OverviewSection key={instance.uid} instance={instance} />
        )}
      </div>
    </div>
  );
}
