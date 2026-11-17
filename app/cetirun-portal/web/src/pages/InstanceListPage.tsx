// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useState } from "react";
import { Link } from "react-router-dom";
import { Button, Spinner, MessageBar, Tooltip } from "@fluentui/react-components";
import { Add20Regular, ChevronRight16Regular } from "@fluentui/react-icons";
import { useInstancesQuery } from "../hooks/useInstance";
import { useFeatures } from "../hooks/useFeatures";
import { CreateForm } from "../components/CreateForm";
import { GovernanceNotice } from "../components/GovernanceNotice";
import { LifecycleGlyph, lifecycleState } from "../components/Lifecycle";
import { agentTypeLabel } from "../utils/instance";

export function InstanceListPage() {
  const { data: instances, isLoading, error } = useInstancesQuery();
  const { data: features } = useFeatures();
  const [showCreate, setShowCreate] = useState(false);

  const maxInstances = features?.maxInstancesPerUser ?? 1;
  const atLimit = maxInstances > 0 && (instances?.length ?? 0) >= maxInstances;

  return (
    <div className="portal-page">
      <div className="portal-page-inner">
        <div className="portal-page-head">
          <div>
            <h1 className="portal-page-title">My Agents</h1>
            {instances && features && (
              <p className="portal-page-count">
                {maxInstances > 0
                  ? `${instances.length} of ${maxInstances} agents`
                  : `${instances.length} agent${instances.length === 1 ? "" : "s"}`}
              </p>
            )}
          </div>
          <Tooltip
            content={`Limit of ${maxInstances} agent${maxInstances > 1 ? "s" : ""} reached`}
            relationship="description"
            visible={atLimit ? undefined : false}
          >
            <Button
              icon={<Add20Regular />}
              appearance="primary"
              disabled={atLimit}
              onClick={() => setShowCreate(true)}
            >
              New Agent
            </Button>
          </Tooltip>
        </div>

        <div className="portal-stack">
          {isLoading && <Spinner label="Loading agents..." />}

          {error && (
            <MessageBar intent="error">
              Failed to load agents: {(error as Error).message}
            </MessageBar>
          )}

          {showCreate && (
            <section className="portal-panel portal-panel-body" aria-labelledby="new-agent-title">
              <h2 id="new-agent-title" className="portal-panel-title">New agent</h2>
              <CreateForm onCreated={() => setShowCreate(false)} onCancel={() => setShowCreate(false)} />
            </section>
          )}

          {instances && instances.length === 0 && !showCreate && (
            <div className="portal-empty">
              You don&apos;t have any agents yet. Create one to get started.
            </div>
          )}

          {!!instances?.length && (
            <div className="portal-panel agent-list">
              {instances.map((inst) => (
                <div key={inst.instanceName} role="group" aria-label={inst.instanceName} className="agent-row">
                  <LifecycleGlyph state={lifecycleState(inst.phase)} size={18} />
                  <div>
                    <Link to={`/instances/${inst.instanceName}/overview`} className="agent-row-name">
                      {inst.instanceName}
                    </Link>
                    <span className="agent-row-display">{inst.displayName}</span>
                    <GovernanceNotice instance={inst} compact />
                  </div>
                  <div className="agent-row-facts">
                    <span className="agent-row-type">{agentTypeLabel(inst.agent.type)}</span>
                    <span className="agent-row-phase">{inst.phase}</span>
                  </div>
                  <ChevronRight16Regular className="agent-row-chevron" />
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
