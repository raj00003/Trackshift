#!/usr/bin/env python3
"""
Pilot scenario helper for Trackshift.

Usage:
    python tools/demo-scripts/pilot.py --scenario f1
"""

from __future__ import annotations

import argparse
import json
import time
import urllib.error
import urllib.request
from typing import Any, Dict

SCENARIOS: Dict[str, Dict[str, Any]] = {
    "f1": {
        "intent": {
            "classification": "critical",
            "priority": "latency",
            "pqc": True,
            "notes": "F1 telemetry push",
        },
        "files": [
            {
                "name": "f1-telemetry-0001.bin",
                "sizeBytes": 175_000_000,
                "mime": "application/octet-stream",
                "critical": True,
                "challenges": "latency-sensitive",
            }
        ],
        "metadata": {"scenario": "f1", "description": "Pit wall to HQ stream"},
    },
    "clinic": {
        "intent": {
            "classification": "medical",
            "priority": "reliability",
            "pqc": True,
            "notes": "Remote clinic DICOM",
        },
        "files": [
            {
                "name": "clinic-dicom-2025-11-15.tar.zst",
                "sizeBytes": 9_800_000_000,
                "mime": "application/tar+zstd",
                "critical": False,
                "challenges": "reliability-sensitive",
            }
        ],
        "metadata": {"scenario": "clinic", "department": "radiology"},
    },
}


def request(
    base_url: str,
    method: str,
    path: str,
    *,
    data: Dict[str, Any] | None,
    api_key: str,
    mfa: str,
    tenant: str,
) -> Dict[str, Any]:
    url = f"{base_url.rstrip('/')}{path}"
    payload = json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(url, data=payload, method=method)
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", f"Bearer {api_key}")
    req.add_header("X-MFA-Token", mfa)
    req.add_header("X-Tenant-ID", tenant)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:  # pragma: no cover - convenience tool
        body = exc.read().decode()
        raise SystemExit(f"{exc.code} {exc.reason}: {body}")


def poll_job(base_url: str, job_id: str, api_key: str, mfa: str, tenant: str) -> Dict[str, Any]:
    for _ in range(60):
        job = request(base_url, "GET", f"/jobs/{job_id}", data=None, api_key=api_key, mfa=mfa, tenant=tenant)
        state = job["state"]
        if state in {"COMPLETED", "FAILED", "CANCELED"}:
            return job
        time.sleep(1)
    raise TimeoutError(f"job {job_id} did not finish in time")


def main() -> None:
    parser = argparse.ArgumentParser(description="Run Trackshift pilot scenarios.")
    parser.add_argument("--scenario", choices=sorted(SCENARIOS), default="f1")
    parser.add_argument("--base-url", default="http://127.0.0.1:8090")
    parser.add_argument("--api-key", default="local-admin-token")
    parser.add_argument("--mfa", default="000000", help="MFA token or ORCH_MFA_BYPASS value")
    parser.add_argument("--tenant", default="pilot")
    args = parser.parse_args()

    payload = SCENARIOS[args.scenario]
    print(f"Submitting {args.scenario} job to {args.base_url} as tenant {args.tenant}")
    job = request(
        args.base_url, "POST", "/jobs", data=payload, api_key=args.api_key, mfa=args.mfa, tenant=args.tenant
    )
    job_id = job["id"]
    print(f"Job {job_id} queued with predicted path {job['prediction']['path']}")

    final = poll_job(args.base_url, job_id, args.api_key, args.mfa, args.tenant)
    receipt = final.get("receipt")
    print(
        json.dumps(
            {
                "jobId": job_id,
                "state": final["state"],
                "latencyMillis": final.get("latencyMillis"),
                "path": final["prediction"]["path"],
                "receipt": receipt,
            },
            indent=2,
        )
    )
    if receipt:
        print(f"Receipt stored. Signature algorithm: {receipt['signatureAlg']}")
    else:
        print("No receipt available (job failed or canceled).")


if __name__ == "__main__":  # pragma: no cover
    main()
