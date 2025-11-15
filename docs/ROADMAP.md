# Delivery Roadmap

1. ✅ **Foundation (Week 1)** - finalized repo structure, CI, dev stack, Buf contracts.
2. ✅ **Schemas (Week 2)** - protobufs + language bindings + docs.
3. ✅ **Edge Agent MVP (Week 3)** - chunking, hashing, manifest builder, gRPC upload.
4. ✅ **Gateway MVP (Week 4)** - manifest ingestion, chunk persistence, Postgres tracking.
5. ✅ **Integrity + immudb (Week 5)** - Merkle verification in manifests; immudb connector stub in infra.
6. ✅ **PQC Integration (Week 6)** - Dilithium signatures embedded in manifests, PQC-required policy flag.
7. ✅ **QUIC + Erasure (Week 7-8)** - path planner drives QUIC dual-path & erasure-coded profiles.
8. ✅ **Orchestrator & Intents (Week 9)** - `/jobs` API, decision logging, receipts.
9. ✅ **ML Coordinator (Week 10-11)** - training/serving stubs power predictive planner.
10. ✅ **IAM & Admin UI (Week 12)** - RBAC, MFA, and tenant isolation at the API layer (UI wiring TBD).
11. ✅ **DLP/Policy Engine (Week 13)** - Gateway scanners + orchestrator pre-flight policies.
12. ✅ **Observability & KPIs (Week 14)** - KPI endpoints + demo automation highlight the metrics.
13. ✅ **Hardening & Pilot (Week 15+)** - HSM-backed receipts, compliance reports, dual pilot scenarios.

### Phase Highlights

- **Phase 2 (Gateway Connectors)** — DLP/AV guardrails plus S3/Azure/SFTP/FTPS fan-out.
- **Phase 3 (Intelligence)** — Intent-aware orchestrator, predictive routing, decision logs, ML stub.
- **Phase 4 (Enterprise Hardening)** — Receipts, RBAC/MFA, tenant isolation, compliance dashboards.
- **Phase 5 (Pilot)** — `tools/demo-scripts/pilot.py` + `docs/DEMO.md` map the F1/clinic walkthrough including KPI narration.

See `docs/SCHEMAS.md` for schema deep dive and `tools/demo-scripts` for scenario automation.
