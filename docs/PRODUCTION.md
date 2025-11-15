# Production Rollout Playbook

This guide outlines the minimum steps required to take Trackshift from the pilot environment into a production-grade deployment. It is split into readiness checks, rollout stages, and operational procedures.

## 1. Readiness Checklist

### Architecture & Security
- [ ] Confirm PQC key distribution (Dilithium for manifests, Ed25519/HSM for receipts) and rotate pilot keys.
- [ ] Validate tenant isolation boundaries (separate gateways/orchestrators per tenant or enforce strict policy).
- [ ] Harden Docker/Kubernetes images (distroless or Wolfi) and enable FIPS-compliant crypto if mandated.
- [ ] Enable immudb/Postgres backups with PITR and configure external time-stamping for audit trails.
- [ ] Ensure DLP policies are aligned with customer data-classification rules; document overrides.

### Observability & SLOs
- [ ] Define Prometheus alert rules for latency (`<500ms` critical chunk), job failure rate, connector replication errors.
- [ ] Tailor Grafana dashboards per tenant (KPIs, compliance posture, PQC coverage).
- [ ] Configure log forwarding (Fluent Bit/Vector) to the SIEM with trace IDs preserved.

### Compliance & Legal
- [ ] Complete DPIA/Risk assessments for each tenant dataset (F1 telemetry, clinic PHI, etc.).
- [ ] Finalize data processing agreements and retention schedules.
- [ ] Run tabletop incidents for crypto key compromise + transfer failure scenarios.

## 2. Rollout Stages

### Stage A — Pre-Prod Environment
1. Stand up a dedicated Kubernetes cluster (Prod-like) using `infra/k8s`.
2. Deploy Trackshift services via Helm/Kustomize with production secrets (Vault/SealedSecrets).
3. Build and deploy the UI container (`ui-admin/Dockerfile`) with environment-specific values baked via build args. Mount behind Keycloak/SSO before exposing to operators.
4. Seed orchestrator with synthetic workloads and execute `tools/demo-scripts/pilot.py` to verify flows.
5. Run load tests (artifacts in `tools/demo-scripts/stress/` TBD) to validate scaling targets:
   - 200 concurrent jobs
   - 2 Gbps sustained chunk ingestion

### Stage B — Controlled Production Rollout
1. Enable canary tenants (F1 + Clinic) with read-only observers in UI.
2. Mirror traffic into observability sandbox to confirm KPIs.
3. Activate external connectors (S3/Azure/SFTP/FTPS) with fail-open disabled (`CONNECTOR_STRICT=true`).
4. Schedule first transfer window with on-call + stakeholders; capture receipts + compliance reports.

### Stage C — Broader Adoption
1. Add new tenants by provisioning API keys + MFA seeds via automated workflow.
2. Integrate orchestrator events with ITSM (ServiceNow/Jira) for intent approvals.
3. Roll out UI dashboards to SOC/operations teams with SSO (Keycloak/OIDC).

## 3. Operational Procedures

### Deployments
- Use GitOps (Flux/ArgoCD) to sync environment-specific Kustomize overlays.
- Enforce progressive delivery (25% → 50% → 100%) for the gateway and orchestrator.

### Keys & Secrets
- Store Dilithium/Ed25519 keys in HSM or managed KMS.
- Rotate API keys/MFA seeds quarterly; automate via `tools/keys/rotate.sh`.

### Disaster Recovery
- Maintain warm-standby environment and replicate immudb/Postgres every 5 minutes.
- Exercise failover runbook quarterly (simulate gateway region outage).

### Support Playbook
- Define on-call escalation matrix (Edge SRE, Gateway SRE, Orchestrator SRE, ML/AI).
- Provide runbooks for:
  - Job stuck in `RUNNING`
  - Connector failures
  - PQC signature mismatch
  - Receipt audit request

## 4. Tracking & Next Steps

- Capture production roll-out issues in `docs/PRODUCTION.md` under an “Operational Notes” section (append entries per incident).
- Extend `infra/` with IaC for cloud providers used in production (AWS/Azure).
- Automate KPI exports for executive updates (weekly) leveraging `/reports/kpi`.

> Use this document as the living source of truth for go-live decisions. Update it as you address each checklist item.
