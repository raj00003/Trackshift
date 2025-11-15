# Demo Scripts

This folder contains pilot scenario helpers (F1 telemetry, clinic DICOM uploads).

`pilot.py` is a quick CLI that drives the orchestrator API for both scenarios. Example:

```
python tools/demo-scripts/pilot.py --scenario f1 --tenant pilot --mfa 000000
```

The script will:

1. POST a job with the correct intent metadata (classification, PQC flag, policy labels).
2. Poll `/jobs/{id}` until the orchestrator finishes the run and issues a signed receipt.
3. Print a JSON summary (state, latency, predicted path, receipt signature) that you can paste into the deck.

After the job is accepted, run the edge agent with the job ID to push data to the gateway. A sample command is printed in `docs/DEMO.md`.
