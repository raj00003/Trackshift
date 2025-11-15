# Architecture Overview

- **Edge Agent (Rust)** – handles file slicing, hashing, PQC signatures, QUIC transports, TorchScript inference.
- **Gateway (Go)** – receives manifests/chunks, persists to object storage, verifies Merkle roots, logs to immudb, enforces policies + DLP verdicts.
- **Orchestrator (Go)** – intent API, job lifecycle, policy hooks, telemetry aggregation.
- **ML Coordinator (Python)** – trains and distributes path-selection models.
- **Admin UI (React)** – Keycloak-authenticated dashboard for jobs, decision logs, observability.
- **Infra** – Docker Compose for local dev, K8s (Kind/EKS) for deployments, Terraform for cloud provisioning, Prometheus/Grafana for metrics.