# Gateway

Go ingestion service exposing the gRPC `ManifestIngestService` defined in `proto/trackshift/manifest/manifest.proto`. It accepts manifests, streams of file chunks, persists them to disk, and records transfer state in Postgres.

## Running Locally

```
POSTGRES_DSN="postgres://trackshift:trackshift@localhost:5432/trackshift?sslmode=disable" \
  go run ./cmd/gateway
```

Environment variables:

- `GATEWAY_BIND` (default `:50051`) – gRPC listen address.
- `DATA_DIR` (default `data/jobs`) – filesystem root for manifests + chunks.
- `POSTGRES_DSN` – connection string for the tracking database (see `infra/docker-compose` for local defaults).

The gateway automatically creates a simple `transfers` table and upserts rows keyed by `job_id` whenever manifests or chunks are received. All artifacts are stored under `DATA_DIR/<job_id>/`.
