---
title: "Roadmap"
description: "Improvements that are planned for CetiRun and that are not available yet."
weight: 40
status: planned
---

**Status: planned.** These directions are not a list of available features or
a commitment to release dates. For the features that CetiRun has, read the
[Overview](../).

- **More execution targets:** self-managed Kubernetes with KVM or Firecracker,
  in addition to AKS.
- **Usage reports:** model spend and use for each Agent, user, and Team in the
  Portal.
- **Shared agent pools:** an Agent mode with many interchangeable replicas for
  headless, queue-driven work. The replicas use throwaway home storage instead
  of a private volume and have no inbound route. They share one AI Gateway key
  that records the usage of each task.
- **Approved operations:** a Portal step in which a platform administrator
  approves each change that CetiAdmin, the operations Agent of the platform,
  proposes.
