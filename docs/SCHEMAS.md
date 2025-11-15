# Schemas & Contracts

## Manifest

| Field | Type | Notes |
| ----- | ---- | ----- |
| `job_id` | string | Matches orchestrator job id |
| `chunk_size` | uint32 | Bytes per chunk, default 4 MiB |
| `chunk_refs` | repeated ChunkRef | Contains index/hash/offset/hints |
| `merkle_root` | bytes | Root of chunk hashes |
| `dilithium_sig` | bytes | PQ signature over canonical manifest |
| `sender_id` | string | Edge identity |

Example manifest:
```json
{
  "jobId": "job-f1",
  "chunkSize": 4194304,
  "merkleRoot": "9e5f...",
  "chunkRefs": [
    {"index":0,"chunkHash":"a7f1...","offset":0,"length":4194304}
  ],
  "dilithiumSig": "MEQCID...",
  "senderId": "edge-nyc-01"
}
```

## Intent & JobSpec

Intents capture urgency, deadline, cost ceiling, reliability level, data sensitivity, and free-form attributes. Job specs bundle intents with file specs (uri, redundancy target, chunk size, PQC requirement) and optional path requirements.

Sample request:
```json
{
  "spec": {
    "jobId": "job-clinic-42",
    "intent": {
      "urgency": "CRITICAL",
      "deadline": "2024-07-01T12:30:00Z",
      "costCeiling": 150.0,
      "reliabilityLevel": "FIVE_NINES",
      "sensitivity": "PHI",
      "regionPolicy": "stay-us-east",
      "attributes": [{"key":"path.diversity","value":">=3"}]
    },
    "files": [{
      "uri": "file:///data/sample.mp4",
      "redundancyTarget": "6/4",
      "chunkSize": 4194304,
      "pqcRequired": true
    }]
  }
}
```

## Decision Log

Each transfer emits a `DecisionLog` with path selections (`score`, `reason`), policy and DLP events, and telemetry metrics (latency, loss, cost). Logs are written to Postgres + immudb for audit-grade retention and surfaced in the admin UI.

## Error Codes

See `proto/trackshift/common/common.proto` for canonical `ErrorCode` enums (intent invalid, policy denied, upload rejected, signature invalid, integrity failed, internal). All APIs return `error_code` + human-readable message for debuggability.
