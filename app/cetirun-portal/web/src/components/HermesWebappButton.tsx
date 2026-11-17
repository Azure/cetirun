// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useRef, useState } from "react";
import { Button } from "@fluentui/react-components";
import { ArrowUpRight16Regular, Chat20Regular } from "@fluentui/react-icons";
import { signInToHermes } from "../api/hermes";
import type { Instance } from "../types/instance";

type Props = {
  instance: Pick<Instance, "uid" | "instanceName">;
  disabled: boolean;
};

export function HermesWebappButton({ instance, disabled }: Props) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  const scope = `${instance.uid}:${instance.instanceName}:${disabled}`;
  const currentScope = useRef(scope);
  currentScope.current = scope;

  useEffect(() => {
    setPending(false);
    setError("");
    return () => {
      request.current?.abort();
      request.current = null;
    };
  }, [scope]);

  const open = async () => {
    if (disabled || request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    setError("");
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15000)]);
    const active = () => request.current === controller && currentScope.current === scope && !controller.signal.aborted;
    try {
      const target = await signInToHermes(instance, signal);
      if (active() && !signal.aborted) window.location.assign(target);
    } catch (error) {
      if (active()) {
        const timedOut = (error instanceof Error || error instanceof DOMException) && error.name === "TimeoutError";
        setError(timedOut
          ? "Hermes sign-in timed out. Please try again."
          : error instanceof Error ? error.message : "Could not sign in to the Hermes Webapp. Please try again.");
      }
    } finally {
      if (active()) {
        request.current = null;
        setPending(false);
      }
    }
  };

  return <>
    <Button
      type="button"
      className="portal-side-link"
      appearance="transparent"
      icon={<Chat20Regular />}
      disabled={disabled || pending}
      aria-busy={pending}
      onClick={() => void open()}
    >
      {pending ? "Opening Agent UI..." : "Agent UI"}
      <ArrowUpRight16Regular className="portal-side-link-out" />
    </Button>
    {error && <span role="alert" className="portal-side-note">{error}</span>}
  </>;
}
