# Edge Agent

Rust CLI that slices files, builds Blake3/Merkle manifests, and streams chunks to the gateway over gRPC.

```bash
cargo run -p edge-agent -- \
  --file sample.mp4 \
  --gateway http://127.0.0.1:50051 \
  --out-dir artifacts
```

Outputs live under `artifacts/<job_id>/` with chunk binaries and `manifest.json` while the gateway simultaneously receives the manifest/chunk stream.
