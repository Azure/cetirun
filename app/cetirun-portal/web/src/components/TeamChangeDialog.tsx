// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Dialog, DialogActions, DialogBody, DialogContent, DialogSurface, DialogTitle, Field, MessageBar, Select, Spinner, Switch, Text } from "@fluentui/react-components";
import { getTeamChangeByName, requestTeamChangeByName } from "../api/instance";
import { PortalError } from "../api/fetch";
import { useCreateOptions } from "../hooks/useCreateOptions";
import { useTeams } from "../hooks/useTeams";
import type { ModelAccess, TeamChangeOperation, TeamChangeRequest } from "../types/instance";

interface SubmittedTeamChange {
  request: TeamChangeRequest;
  requestID: string;
}

function newRequestID(): string {
  return crypto.randomUUID().replace(/-/g, "");
}

function operationMessage(operation?: TeamChangeOperation): string | undefined {
  const status = operation?.status;
  if (!status) return undefined;
  if (status.phase === "Failed") {
    const detail = status.failureMessage || status.failureReason || "The Team change failed.";
    return operation?.pending ? `${detail} The Agent remains safely held while the original operation is retried or resolved.` : detail;
  }
  if (status.phase === "Completed") return "Team change completed.";
  return `Team change is ${status.phase.toLowerCase()}. The Agent may be temporarily unavailable.`;
}

export function TeamChangeDialog({ instanceName, open, onOpenChange, onSubmitted }: {
  instanceName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmitted?: () => void;
}) {
  const queryClient = useQueryClient();
  const [targetTeam, setTargetTeam] = useState("");
  const [targetSandbox, setTargetSandbox] = useState("");
  const [model, setModel] = useState("");
  const [useSharedModels, setUseSharedModels] = useState(true);
  const [useCopilot, setUseCopilot] = useState(true);
  const [submitted, setSubmitted] = useState<SubmittedTeamChange | null>(null);
  const operationQuery = useQuery({
    queryKey: ["instance", instanceName, "team-change"],
    queryFn: () => getTeamChangeByName(instanceName),
    enabled: open,
    refetchInterval: (query) => query.state.data?.pending ? 5_000 : false,
    retry: false,
  });
  const teamsQuery = useTeams();
  const targetOptions = useCreateOptions(targetTeam, open && Boolean(targetTeam));
  const operation = operationQuery.data;
  const pending = operation?.pending ?? false;
  const currentSandbox = operation?.sandbox || "";
  const options = targetOptions.data;
  const allowedSandboxes = options?.allowedSandboxes || [];
  const catalog = options?.gateway;
  const catalogKnown = !!catalog?.known;
  const sharedModels = useMemo(() => catalog?.known ? catalog.sharedModels : [], [catalog]);
  const sharedAvailable = sharedModels.length > 0;
  const copilotOffered = catalogKnown && !!catalog?.githubCopilot;
  const sharedForced = !copilotOffered && sharedAvailable;
  const effectiveShared = sharedAvailable && (sharedForced || useSharedModels);
  const copilotForced = copilotOffered && (!effectiveShared || !sharedAvailable);
  const effectiveCopilot = copilotOffered && (copilotForced || useCopilot);
  const noModelSource = catalogKnown && !sharedAvailable && !copilotOffered;
  const modelRouters = useMemo(() => catalogKnown ? options?.modelRouters ?? [] : [], [catalogKnown, options?.modelRouters]);
  const models = useMemo(() => [
    ...(effectiveShared ? sharedModels.map((candidate) => candidate.name) : []),
    ...modelRouters,
  ], [effectiveShared, modelRouters, sharedModels]);
  const targetBlocked = !options || !!options.blocked || (options.aiGatewayEnabled && (!catalogKnown || noModelSource));
  const controlsLocked = submitted !== null || pending || operationQuery.isLoading;

  useEffect(() => {
    if (!options) return;
    setTargetSandbox((current) => {
      if (current && allowedSandboxes.includes(current)) return current;
      if (allowedSandboxes.includes(currentSandbox)) return currentSandbox;
      return options.defaultSandbox;
    });
  }, [allowedSandboxes, currentSandbox, options]);

  useEffect(() => {
    if (model && !models.includes(model)) setModel("");
  }, [model, models]);

  useEffect(() => {
    if (submitted && operation && !pending &&
      (operation.request?.requestID === submitted.requestID || operation.status?.requestID === submitted.requestID)) setSubmitted(null);
  }, [operation, pending, submitted]);

  const mutation = useMutation({
    mutationFn: (request: SubmittedTeamChange) => requestTeamChangeByName(instanceName, request.request, request.requestID),
    onSuccess: (accepted) => {
      queryClient.setQueryData(["instance", instanceName, "team-change"], accepted);
      queryClient.invalidateQueries({ queryKey: ["instance", instanceName] });
      queryClient.invalidateQueries({ queryKey: ["instances"] });
      queryClient.invalidateQueries({ queryKey: ["instance", instanceName, "team-change"] });
      onSubmitted?.();
    },
    onError: (error) => {
      if (error instanceof PortalError && [400, 403, 404, 409, 428].includes(error.status)) {
        setSubmitted(null);
        queryClient.invalidateQueries({ queryKey: ["instance", instanceName, "team-change"] });
      }
    },
  });

  const canSubmit = Boolean(targetTeam && targetSandbox && operation && !pending && !targetBlocked && !mutation.isPending && !operationQuery.isFetching);
  const submit = () => {
    if (!canSubmit) return;
    const next = submitted ?? {
      requestID: newRequestID(),
      request: {
        uid: operation!.uid,
        resourceVersion: operation!.resourceVersion,
        targetTeam,
        targetSandbox,
        model: model || undefined,
        modelAccess: { sharedModels: effectiveShared, githubCopilot: effectiveCopilot } satisfies ModelAccess,
      },
    };
    if (!submitted) setSubmitted(next);
    mutation.mutate(next);
  };

  return <Dialog open={open} onOpenChange={(_, data) => onOpenChange(data.open)}>
    <DialogSurface>
      <DialogBody>
        <DialogTitle>Change Team</DialogTitle>
        <DialogContent>
          <Text block>This safely stops the Agent, applies the target Team policy, then restores it if it was running. The Agent may be temporarily unavailable. HOME data is retained; incompatible storage is rejected before the Agent stops.</Text>
          {operationQuery.isLoading && <Spinner label="Loading Team change status..." />}
          {operationQuery.error && <MessageBar intent="error">{operationQuery.error.message}</MessageBar>}
          {operationMessage(operation) && <MessageBar intent={operation?.status?.phase === "Failed" ? "error" : "info"}>{operationMessage(operation)}</MessageBar>}
          {!pending && operation && <div style={{ display: "flex", flexDirection: "column", gap: 12, marginTop: 16 }}>
            <Field label="Target Team" required>
              <Select value={targetTeam} onChange={(event) => { setTargetTeam(event.target.value); setModel(""); }} disabled={teamsQuery.isLoading || controlsLocked || mutation.isPending}>
                <option value="">Choose a Team</option>
                {(teamsQuery.data || []).map((team) => <option key={team.name} value={team.name}>{team.name}</option>)}
              </Select>
            </Field>
            {targetTeam && targetOptions.isFetching && <Spinner size="tiny" label="Loading Team policy..." />}
            {options?.blocked && <MessageBar intent="error">{options.blocked.message}</MessageBar>}
            {targetTeam && options && <>
              <Field label="Sandbox" required hint={allowedSandboxes.includes(currentSandbox) ? "Your current compatible Sandbox is selected." : "The target Team default Sandbox is selected."}>
                <Select value={targetSandbox} onChange={(event) => setTargetSandbox(event.target.value)} disabled={controlsLocked || mutation.isPending}>
                  {allowedSandboxes.map((sandbox) => <option key={sandbox} value={sandbox}>{sandbox}{sandbox === options.defaultSandbox ? " (default)" : ""}</option>)}
                </Select>
              </Field>
              {options.aiGatewayEnabled && catalogKnown && !noModelSource && <>
                {sharedAvailable && <Field hint={`Models your administrators share: ${sharedModels.map((candidate) => candidate.name).join(", ")}`}>
                  <Switch checked={effectiveShared} disabled={controlsLocked || sharedForced} onChange={(_, data) => setUseSharedModels(!!data.checked)} label="Use shared models" />
                </Field>}
                {copilotOffered && <Field hint={copilotForced ? "Required when shared models are off or none are available." : "Uses the Agent owner's GitHub Copilot subscription after sign-in."}>
                  <Switch checked={effectiveCopilot} disabled={controlsLocked || copilotForced} onChange={(_, data) => setUseCopilot(!!data.checked)} label="Use GitHub Copilot" />
                </Field>}
                {models.length > 0 && <Field label="Model (optional)" hint="Keep the current model, or select a model permitted by the target Team.">
                  <Select value={model} onChange={(event) => setModel(event.target.value)} disabled={controlsLocked || mutation.isPending}>
                    <option value="">Keep current model</option>
                    {models.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}
                  </Select>
                </Field>}
              </>}
              {options.aiGatewayEnabled && !catalogKnown && !targetOptions.isFetching && <MessageBar intent="warning">The AI Gateway has not published its models yet. Try again in a minute.</MessageBar>}
              {options.aiGatewayEnabled && noModelSource && <MessageBar intent="error">No models are available under the selected Team&apos;s Policy and deployment settings.</MessageBar>}
            </>}
          </div>}
          {mutation.error && <MessageBar intent="error">{mutation.error.message} {submitted ? "Retry sends the same Team change request." : "Review the current selection before submitting again."}</MessageBar>}
        </DialogContent>
        <DialogActions>
          <Button appearance="secondary" onClick={() => onOpenChange(false)} disabled={mutation.isPending}>Close</Button>
          {!pending && <Button appearance="primary" disabled={!canSubmit} onClick={submit}>{mutation.isPending ? "Requesting..." : submitted && mutation.isError ? "Retry request" : "Change Team"}</Button>}
        </DialogActions>
      </DialogBody>
    </DialogSurface>
  </Dialog>;
}
