# 10-Minute Pilot Demo

This walk-through proves the KPIs, policy guardrails, and receipts for both reference scenarios.

## Environment Prep

1. Start the gateway and edge agent (from repo root):
   ```bash
   POSTGRES_DSN="postgres://trackshift:trackshift@localhost:5432/trackshift?sslmode=disable" \
     go run ./gateway/cmd/gateway

   cargo run -p edge-agent -- --file sample.mp4 --gateway http://127.0.0.1:50051 --out-dir artifacts
   ```
2. Start the orchestrator with policy + MFA + tenant isolation (new terminal):
   ```bash
   export ORCH_HTTP_ADDR=":8090"
   export ORCH_API_KEYS="admin:local-admin-token,viewer:local-observer"
   export ORCH_MFA_BYPASS="000000"
   export POLICY_ALLOWED_CLASSES="pilot:critical|medical"
   go run ./orchestrator/cmd/orchestrator
   ```
3. (Optional) Launch the ML coordinator stub so `/reports/kpi` shows live metrics:
   ```bash
   uv run ml-coordinator/app/main.py serve --port 8088
   ```

## Scenario A — F1 Telemetry (Latency KPI)

1. Submit the job:
   ```bash
   python tools/demo-scripts/pilot.py --scenario f1 --tenant pilot --mfa 000000
   ```
2. Observe output:
   - Job transitions `QUEUED → RUNNING → COMPLETED`.
   - Prediction picks the `f1-ultra-low-latency` path with `dual-path-quic` guard.
   - Receipt JSON includes Ed25519 signature + hash recorded in `orchestrator/artifacts/receipts/<job>.json`.
3. Trigger the actual transfer using the printed job ID:
   ```bash
   cargo run -p edge-agent -- --job-id <ID> --file ./path/to/f1.bin --gateway http://127.0.0.1:50051
   ```
4. Show KPIs:
   ```bash
   curl -H "Authorization: Bearer local-admin-token" -H "X-MFA-Token: 000000" -H "X-Tenant-ID: pilot" \
     http://127.0.0.1:8090/reports/kpi | jq
   ```
   Confirm:
   - Critical chunk delivered < **500 ms** (latencyMillis in job payload).
   - Success rate ≥ **99%** (for the demo run).

## Scenario B — Remote Clinic DICOM (Reliability KPI)

1. Submit the job:
   ```bash
   python tools/demo-scripts/pilot.py --scenario clinic --tenant pilot --mfa 000000
   ```
2. Orchestrator chooses `clinic-redundant` path (erasure 5+3). Explain how this satisfies ≥99% success under packet loss.
3. Drive the edge agent with the DICOM tarball (or a dummy file) and watch the gateway persist manifest + chunks:
   ```bash
   cargo run -p edge-agent -- --job-id <ID> --file ./path/to/clinic.dcm \
     --gateway http://127.0.0.1:50051 --out-dir artifacts/clinic
   ```
4. Verify compliance/audit:
   ```bash
   curl -H "Authorization: Bearer local-admin-token" -H "X-MFA-Token: 000000" -H "X-Tenant-ID: pilot" \
     "http://127.0.0.1:8090/reports/compliance?tenant=pilot" | jq
   ```
   You should see:
   - Receipts for every transfer (check `receipt` field).
   - Decision log describing policy checks and PQC requirement.
   - `successUnder500` metric for comparison vs. F1 run.

## Auditor Step

1. Pick a receipt file from `orchestrator/artifacts/receipts`.
2. Validate the signature:
   ```bash
   cat orchestrator/artifacts/receipts/public.key  # Base64-encoded Ed25519 public key
   ```
   Use any Ed25519 verifier (or drop into `python -m pynacl`) to recompute the hash and check the signature.
3. Cross-check with the gateway’s manifest + Dilithium signature included in the manifest JSON.

## What This Demonstrates

- **Critical chunk < 500 ms**: shown in job latency for F1.
- **≥99 % success under network outages**: orchestrator failure guard + redundant path for clinic plus compliance report totals.
- **Explainable decisions**: `decisionLog` + `policyDecisions` returned by `/jobs/{id}`.
- **PQC everywhere**: edge manifests carry Dilithium signatures; orchestrator receipts use Ed25519 today with a slot to swap to Dilithium once available.
- **Verifiable receipts**: each transfer yields a signed JSON artifact checked above.
