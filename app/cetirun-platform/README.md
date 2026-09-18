# CetiRun Platform chart

`cetirun-platform` packages the Operator and Portal into one Helm release.
It does not install AKS managed Istio, the CetiRun Istio release, certificates,
credentials, Agent Instances, or retained Agent data.

Create the Operator and Portal namespaces before installation, then use values
which explicitly confirm the separately installed Istio prerequisites:

```bash
helm dependency build app/cetirun-platform
helm upgrade --install cetirun-platform app/cetirun-platform \
  --namespace cetirun-system --create-namespace \
  --set istio.enabled=true \
  --set cetirun-portal.istio.host=cetirun.example.com
```

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.
