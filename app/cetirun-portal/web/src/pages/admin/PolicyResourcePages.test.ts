// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { isDNS1123Subdomain, policyTemplate, resourcePayload, sandboxTemplate } from "./PolicyResourcePages";

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

  it("uses exclusive-attach storage and produces CRD payloads", () => {
    expect(sandboxTemplate.storage.home.accessMode).toBe("ReadWriteOncePod");
    expect(resourcePayload("sandbox", "standard", sandboxTemplate)).toEqual({
      apiVersion: "cetirun.microsoft.com/v1alpha1", kind: "Sandbox", metadata: { name: "standard" }, spec: sandboxTemplate,
    });
  });

  it("accepts Kubernetes DNS subdomain resource names", () => {
    expect(isDNS1123Subdomain("normal.v1")).toBe(true);
    expect(isDNS1123Subdomain("new")).toBe(true);
    expect(isDNS1123Subdomain("Normal.v1")).toBe(false);
    expect(isDNS1123Subdomain(`${"a".repeat(64)}.v1`)).toBe(false);
  });
});
