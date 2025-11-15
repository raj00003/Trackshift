# Trackshift Platform – Full System Guide

Trackshift is a multi-service platform for resilient, policy-aware edge data movement with PQC integrity. This guide explains the full stack (frontend + backend), how data flows through the system, how to run it locally, limitations, and real-world use cases.

---

## Architecture at a Glance
- **Edge Agent (Rust)**: Slices files, hashes chunks, builds Merkle manifests, signs manifests (Dilithium-ready), streams manifest + chunks over gRPC to the gateway, and writes artifacts locally.
- **Gateway (Go)**: gRPC ingest for manifests/chunks; persists to disk; records transfers in Postgres; optional DLP/AV checks; optional replication to S3/Azure/SFTP/FTPS.
- **Orchestrator (Go)**: REST API for jobs/intents; policy evaluation; predictive routing (plugs into ML coordinator); issues signed receipts (Ed25519, HSM-ready); compliance/KPI endpoints.
- **ML Coordinator (Python)**: Trains/serves a softmax-based routing predictor; publishes predictions to orchestrator when `ML_COORDINATOR_URL` is set.
- **Admin UI (React + TypeScript, Vite)**: Dashboards KPIs, job list, job details/policy/receipts; buttons to launch pilot scenarios; reads orchestrator REST APIs.
- **Data Services**: Postgres (transfers), MinIO (object store), immudb (integrity runway), Kafka/NATS (messaging), Prometheus/Grafana (observability), Keycloak (IAM ready).

---

## Data Flow (End-to-End)
1. **Job submission**: Via UI or pilot script to orchestrator `/jobs` (REST). Orchestrator runs policies and ML route selection, enqueues work, emits decision log, and prepares a receipt placeholder.
2. **Transfer execution**: Edge Agent reads the file, chunks & hashes it, builds Merkle manifest + optional Dilithium signature, stores artifacts locally, and streams manifest/chunks to the gateway.
3. **Ingest & storage**: Gateway validates/optionally scans (DLP/AV), writes manifest/chunks to `data/jobs/<job>/…`, upserts transfer status in Postgres, optionally fans out to external connectors.
4. **Receipts & integrity**: Orchestrator marks completion, signs a receipt (Ed25519; HSM/KMS pluggable), and exposes it via `/jobs/{id}`. Gateway manifests already contain Merkle roots and PQC fields for downstream verification.
5. **Observability**: Prometheus/Grafana collect metrics; orchestrator exposes KPIs via `/reports/kpi`; UI renders KPIs, jobs, and details in real time.

---

## Stacks and Interfaces
- **Edge Agent**: Rust, tonic gRPC client; CLI flags `--file`, `--gateway`, `--out-dir`, `--job-id?`.
- **Gateway**: Go, gRPC server (`ManifestIngestService`), Postgres (default port mapped to **55432** on host), optional connectors/DLP.
- **Orchestrator**: Go, REST (`/jobs`, `/reports/kpi`, `/reports/compliance`, `/healthz`), receipts (Ed25519), policy engine, worker queue.
- **ML Coordinator**: Python + Typer; `train` writes weights, `serve` exposes `/predict`.
- **Admin UI**: React/TS + Vite; consumes orchestrator REST with headers: `Authorization: Bearer <VITE_API_KEY>`, `X-MFA-Token`, `X-Tenant-ID`; base URL `VITE_API_BASE`.
- **Infra**: Docker Compose (`infra/docker-compose/docker-compose.dev.yml`) stands up Postgres, MinIO, immudb, Kafka, NATS, Prometheus, Grafana, Keycloak.

---

## Running Locally (Dev)
Prereqs: Rust, Go 1.22+, Node 20, Python 3.11 + `uv`, Docker Desktop, Buf CLI.

1) **Start infra** (Postgres on host port **55432**, plus MinIO, Kafka, etc.):
```bash
cd trackshift
make dev-up
```

2) **Gateway** (new terminal):
```bash
cd gateway
set POSTGRES_DSN=postgres://trackshift:trackshift@localhost:55432/trackshift?sslmode=disable
go run ./cmd/gateway
```

3) **Edge Agent demo** (new terminal; use a real file path):
```bash
cd trackshift/edge-agent
cargo run -- --file "C:\path\to\file.bin" --gateway http://127.0.0.1:50051 --out-dir artifacts
```

4) **ML Coordinator** (new terminal):
```bash
cd trackshift
uv run ml-coordinator/app/main.py train
uv run ml-coordinator/app/main.py serve --port 8088
```

5) **Orchestrator** (new terminal):
```bash
cd trackshift/orchestrator
set ORCH_HTTP_ADDR=:8090
set ORCH_API_KEYS=admin:local-admin-token,viewer:local-observer
set ORCH_MFA_BYPASS=000000
set ML_COORDINATOR_URL=http://127.0.0.1:8088
go run ./cmd/orchestrator
```

6) **Pilot scenarios** (new terminal):
```bash
cd trackshift
python tools/demo-scripts/pilot.py --scenario f1 --base-url http://127.0.0.1:8090 --mfa 000000 --tenant pilot
python tools/demo-scripts/pilot.py --scenario clinic --base-url http://127.0.0.1:8090 --mfa 000000 --tenant pilot
```

7) **Admin UI** (new terminal):
```bash
cd trackshift/ui-admin
copy .env.example .env  # set VITE_API_BASE=http://127.0.0.1:8090 and auth headers
npm install
npm run dev   # open printed URL (e.g., http://localhost:4173)
```

---

## Metrics & What They Mean (UI)
- **Success Rate**: Completed / total jobs; target ≥ 99% for reliability.
- **P95 Latency**: 95th percentile end-to-end latency; target < 500 ms for critical paths.
- **<500 ms Ratio**: Share of jobs meeting the 500 ms SLO; shows critical-path coverage.
- **Failures (24h)**: Failed jobs in the last day; highlights stability issues.
- **Jobs Table**: Live list with state, path choice, latency; filter by state.
- **Details Panel**: Intent payload, policy decisions, decision log, and signed receipt when a job is selected.

---

## Data Contracts (at a high level)
- **Gateway gRPC**: `UploadManifest`, `UploadChunk` (manifest contains job_id, file_name, size, chunk refs, Merkle root, Dilithium sig fields).
- **Orchestrator REST**:
  - `POST /jobs` → creates job (intent, files, metadata).
  - `GET /jobs` → list jobs with state/prediction/receipt status.
  - `GET /jobs/{id}` → job details + receipt + decision log.
  - `GET /reports/kpi`, `GET /reports/compliance`.
- **UI headers**: `Authorization: Bearer <VITE_API_KEY>`, `X-MFA-Token`, `X-Tenant-ID`.

---

## Limitations (Dev)
- Postgres is exposed on host port 55432; dev auth is permissive compared to prod.
- DLP/AV scanning is basic (pattern/extension checks); use real scanners in prod.
- Connectors (S3/Azure/SFTP/FTPS) are best-effort; harden creds/SSL and set `CONNECTOR_STRICT=true` for fail-safe replication.
- Receipts use Ed25519 by default; swap to HSM/KMS/PQC for production compliance.
- UI dev server (`npm run dev`) is not production-hardened; build the Docker image for deployment.

---

## Real-World Use Cases
- **Latency-sensitive (F1 telemetry)**: Prioritize sub-500 ms delivery using QUIC-like paths and dual-path guard; monitor P95 and <500 ms ratio.
- **Reliability-sensitive (Remote clinic DICOM)**: Use erasure/multipath strategies, high success rate; receipts and policy logs for audit/compliance.
- **Audit & Compliance**: Receipts + decision logs + manifests (Merkle/PQC fields) provide verifiable evidence for regulators or auditors.

---

## Tips for Troubleshooting
- UI shows “Unable to reach orchestrator API” → check `VITE_API_BASE` and auth headers, ensure orchestrator is running, inspect Network tab for `/jobs` and `/reports/kpi`.
- Gateway auth errors → confirm Postgres DSN points to port 55432 and the right credentials.
- ML predictions missing → ensure `ML_COORDINATOR_URL` points to the running `serve` process.
- Connectors failing → set `CONNECTOR_STRICT=false` to allow ingest while you debug replication.

---

## Project Layout (Monorepo)
- `edge-agent/` (Rust) – gRPC client, chunking, Merkle, manifests.
- `gateway/` (Go) – gRPC ingest, storage, Postgres, DLP, connectors.
- `orchestrator/` (Go) – REST API, policy, ML integration, receipts/KPIs.
- `ml-coordinator/` (Python) – train/serve routing model.
- `ui-admin/` (React/TS) – admin dashboard.
- `proto/` – protobuf contracts + generated bindings.
- `infra/` – docker-compose dev stack + Prom/Grafana.
- `tools/demo-scripts/` – pilot scenario drivers.

Run `npm run dev` in `ui-admin` after configuring `.env`, and follow the run steps above for a full end-to-end demo. For production, harden secrets, disable trust auth, and build/deploy images via your chosen orchestrator.
