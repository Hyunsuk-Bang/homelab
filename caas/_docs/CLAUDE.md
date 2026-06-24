# _docs

Design notes and history. Read these for the "why".

- `DESIGN.md` — overall architecture, CRD schemas, reconcile/allocation design.
- `PHASE1.md` — how to run the operator + what Phase 1 delivered.
- `TROUBLESHOOTING-initial-cluster.md` — postmortem of the 5 failures hit bringing up the
  first cluster (API-version, VLAN tag, kubelet feature gates, …) and the fixes now baked
  into `internal/render`. **Read before changing the rendered manifests.**
- `network-provider.md` — the vendor-agnostic network provider design + UniFi mapping +
  decisions (firewall deferred).
