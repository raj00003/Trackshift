# Proto Contracts

Shared protobuf definitions for manifests, intents, jobs, and audit logs. We use [Buf](https://buf.build) for linting and multi-language code generation.

## Usage

```bash
buf lint proto
buf generate proto
```

Generated artifacts land in:
- `edge-agent/src/gen` (Rust `prost`/`tonic` bindings)
- `proto/gen/go` (shared Go module imported as `github.com/trackshift/platform/proto/...`)
- `ml-coordinator/app/proto` (Python gRPC stubs)

See `docs/SCHEMAS.md` for field-by-field documentation and JSON examples.
