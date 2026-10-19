// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState } from "react";
import {
  Button, Dialog, DialogActions, DialogBody, DialogContent, DialogSurface, DialogTitle, Text,
} from "@fluentui/react-components";
import { useRestartInstanceByName } from "../hooks/useInstance";
import type { Instance, RestartInstanceRequest } from "../types/instance";

export function RestartAgentButton({ instance }: { instance: Instance }) {
  const restart = useRestartInstanceByName(instance.instanceName);
  const [open, setOpen] = useState(false);
  const [request, setRequest] = useState<RestartInstanceRequest | null>(null);
  const pending = Boolean(
    instance.restartRequestId && instance.restartRequestId !== instance.observedRestartRequestId ||
    restart.data?.restartRequestId && (restart.data.generation ?? 0) >= (instance.generation ?? 0) &&
      restart.data.restartRequestId !== instance.observedRestartRequestId,
  );

  useEffect(() => {
    if (request && instance.uid === request.uid && instance.observedRestartRequestId === request.requestId) {
      setOpen(false);
      setRequest(null);
      restart.reset();
    }
  }, [instance.uid, instance.observedRestartRequestId, request, restart.reset]);

  const openDialog = () => {
    if (!request) {
      setRequest({ uid: instance.uid, resourceVersion: instance.resourceVersion, requestId: crypto.randomUUID() });
      restart.reset();
    }
    setOpen(true);
  };

  return <>
    <Button onClick={openDialog} disabled={!instance.restartAvailable || pending || restart.isPending}>
      Restart Agent
    </Button>
    {pending && <Text role="status">
      Restart requested; waiting for the replacement Pod to become ready. Suspension or deletion takes precedence.
    </Text>}
    <Dialog open={open} onOpenChange={(_, data) => { if (!restart.isPending) setOpen(data.open); }}>
      <DialogSurface>
        <DialogBody>
          <DialogTitle>Restart Agent</DialogTitle>
          <DialogContent>
            Restart this Agent using its current configuration? HOME files and the existing Agent are retained.
            Active sessions will be interrupted. Temporary files in the old Pod are not retained.
            {restart.isError && <Text block role="alert">
              {restart.error.message} Retry sends the same request; closing this dialog does not cancel an accepted restart.
            </Text>}
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setOpen(false)} disabled={restart.isPending}>Cancel</Button>
            <Button appearance="primary" disabled={restart.isPending || request === null}
              onClick={() => {
                if (request) restart.mutate(request, { onSuccess: () => { setOpen(false); setRequest(null); } });
              }}>
              {restart.isPending ? "Requesting restart..." : restart.isError ? "Retry request" : "Restart"}
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  </>;
}
