// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { policyTemplate, resourcePayload, sandboxTemplate } from "./PolicyResourcePages";

describe("policy resource templates", () => {
  it("starts AgentPolicy resources with every supported default control shape", () => {
    expect(policyTemplate).toMatchObject({
      harnesses: { allowed: ["hermes"] },
      skills: { allowed: [] },
      modelAccess: { copilot: { allowed: false }, sharedModels: { allowed: [] } },
      agentIdentity: { allowed: false, required: false, credentialServices: [] },
      entraID: { enabled: false }, sandboxSelection: { allowMultiple: false },
      security: { readOnlyRootFilesystem: true },
      network: { blockedCIDRs: [], blockedFQDNs: [], blockedPorts: [], allowedInternalCIDRs: [], ingress: { mode: "authenticated", requireMTLS: false } },
    });
  });

  it("uses the native writer-exclusion storage mode and produces CRD payloads", () => {
    expect(sandboxTemplate.storage.home.accessMode).toBe("ReadWriteOncePod");
    expect(resourcePayload("sandbox", "standard", sandboxTemplate)).toEqual({
      apiVersion: "cetirun.microsoft.com/v1alpha1", kind: "Sandbox", metadata: { name: "standard" }, spec: sandboxTemplate,
    });
  });
});
