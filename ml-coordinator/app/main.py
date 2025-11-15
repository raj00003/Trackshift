from __future__ import annotations

import json
import math
import pathlib
import random
import threading
import time
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Dict, Iterable, List, Tuple

import typer

app = typer.Typer(help="Trackshift ML coordinator (training + serving)")

MODELS_DIR = pathlib.Path("artifacts/models")
MODELS_DIR.mkdir(parents=True, exist_ok=True)

DEFAULT_FEATURES = ["size_gb", "criticality", "reliability", "priority"]
LABELS = ["f1", "clinic", "standard"]


@dataclass
class Sample:
    features: List[float]
    label: int


class SoftmaxModel:
    def __init__(self, weights: List[List[float]], bias: List[float], feature_names: List[str], labels: List[str]):
        self.weights = weights
        self.bias = bias
        self.feature_names = feature_names
        self.labels = labels

    @classmethod
    def initialize(cls, feature_count: int) -> "SoftmaxModel":
        weights = [[random.uniform(-0.1, 0.1) for _ in range(feature_count)] for _ in LABELS]
        bias = [0.0 for _ in LABELS]
        return cls(weights, bias, DEFAULT_FEATURES, LABELS)

    def predict_proba(self, features: List[float]) -> List[float]:
        logits = []
        for w_row, b in zip(self.weights, self.bias):
            total = sum(w * f for w, f in zip(w_row, features)) + b
            logits.append(total)
        return softmax(logits)

    def predict(self, features: List[float]) -> Tuple[str, float]:
        probs = self.predict_proba(features)
        idx = max(range(len(probs)), key=lambda i: probs[i])
        return self.labels[idx], probs[idx]

    def to_json(self, metrics: Dict[str, float]) -> Dict[str, object]:
        return {
            "weights": self.weights,
            "bias": self.bias,
            "feature_names": self.feature_names,
            "labels": self.labels,
            "metrics": metrics,
            "timestamp": time.time(),
        }

    @classmethod
    def from_file(cls, path: pathlib.Path) -> "SoftmaxModel":
        payload = json.loads(path.read_text())
        return cls(
            weights=payload["weights"],
            bias=payload["bias"],
            feature_names=payload["feature_names"],
            labels=payload["labels"],
        )


def softmax(logits: List[float]) -> List[float]:
    max_logit = max(logits)
    exps = [math.exp(logit - max_logit) for logit in logits]
    denom = sum(exps) or 1.0
    return [value / denom for value in exps]


def load_dataset(path: pathlib.Path) -> List[Sample]:
    if not path.exists():
        typer.echo(f"[WARN] dataset {path} not found, generating synthetic samples")
        payload = generate_synthetic_dataset(300)
    else:
        payload = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    samples: List[Sample] = []
    for row in payload:
        features = [float(row["features"].get(name, 0.0)) for name in DEFAULT_FEATURES]
        label_name = row.get("label", "standard")
        label_index = LABELS.index(label_name) if label_name in LABELS else LABELS.index("standard")
        samples.append(Sample(features, label_index))
    return samples


def generate_synthetic_dataset(count: int) -> List[Dict[str, object]]:
    rows = []
    for _ in range(count):
        scenario = random.choices(["f1", "clinic", "standard"], weights=[0.25, 0.35, 0.4])[0]
        if scenario == "f1":
            features = {
                "size_gb": random.uniform(0.1, 0.6),
                "criticality": 1.0,
                "reliability": random.uniform(0.2, 0.4),
                "priority": 1.0,
            }
        elif scenario == "clinic":
            features = {
                "size_gb": random.uniform(5, 12),
                "criticality": random.uniform(0.4, 0.6),
                "reliability": 1.0,
                "priority": random.uniform(0.3, 0.6),
            }
        else:
            features = {
                "size_gb": random.uniform(1, 4),
                "criticality": random.uniform(0.2, 0.5),
                "reliability": random.uniform(0.6, 0.8),
                "priority": random.uniform(0.4, 0.7),
            }
        rows.append({"features": features, "label": scenario})
    return rows


def train_model(samples: List[Sample], epochs: int = 400, lr: float = 0.05) -> Tuple[SoftmaxModel, Dict[str, float]]:
    if not samples:
        raise ValueError("dataset is empty")
    feature_count = len(samples[0].features)
    model = SoftmaxModel.initialize(feature_count)

    for epoch in range(epochs):
        grad_w = [[0.0 for _ in range(feature_count)] for _ in LABELS]
        grad_b = [0.0 for _ in LABELS]
        total_loss = 0.0

        for sample in samples:
            probs = model.predict_proba(sample.features)
            for idx in range(len(LABELS)):
                target = 1.0 if idx == sample.label else 0.0
                diff = probs[idx] - target
                for feat_idx, value in enumerate(sample.features):
                    grad_w[idx][feat_idx] += diff * value
                grad_b[idx] += diff
            total_loss += -math.log(probs[sample.label] + 1e-9)

        n = float(len(samples))
        for idx in range(len(LABELS)):
            for feat_idx in range(feature_count):
                model.weights[idx][feat_idx] -= lr * (grad_w[idx][feat_idx] / n)
            model.bias[idx] -= lr * (grad_b[idx] / n)

        if epoch % 100 == 0:
            typer.echo(f"[train] epoch={epoch} loss={total_loss/n:.4f}")

    accuracy = evaluate_accuracy(model, samples)
    metrics = {"loss": total_loss / len(samples), "accuracy": accuracy}
    return model, metrics


def evaluate_accuracy(model: SoftmaxModel, samples: Iterable[Sample]) -> float:
    total = 0
    correct = 0
    for sample in samples:
        label, _ = model.predict(sample.features)
        if label == LABELS[sample.label]:
            correct += 1
        total += 1
    return correct / total if total else 0.0


def latest_model_path() -> pathlib.Path | None:
    models = sorted(MODELS_DIR.glob("model-*.json"))
    return models[-1] if models else None


def persist_model(model: SoftmaxModel, metrics: Dict[str, float]) -> pathlib.Path:
    timestamp = int(time.time())
    path = MODELS_DIR / f"model-{timestamp}.json"
    path.write_text(json.dumps(model.to_json(metrics), indent=2))
    latest = MODELS_DIR / "model-latest.json"
    latest.write_text(path.read_text())
    return path


def feature_vector(payload: Dict[str, float]) -> List[float]:
    return [float(payload.get(name, 0.0)) for name in DEFAULT_FEATURES]


class PredictorHandler(BaseHTTPRequestHandler):
    model_lock = threading.Lock()
    model: SoftmaxModel | None = None

    def log_message(self, format: str, *args) -> None:  # noqa: A003
        return  # silence default logging to keep output clean

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            self._respond(200, {"status": "ok"})
        else:
            self._respond(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/predict":
            self._respond(404, {"error": "not found"})
            return
        length = int(self.headers.get("content-length", "0"))
        payload = json.loads(self.rfile.read(length) or "{}")
        features = payload.get("features")
        if not isinstance(features, dict):
            self._respond(400, {"error": "features object required"})
            return
        model = self._ensure_model()
        if model is None:
            self._respond(503, {"error": "model not loaded"})
            return
        vector = feature_vector(features)
        label, confidence = model.predict(vector)
        response = format_prediction(label, confidence, payload.get("context", {}))
        self._respond(200, response)

    def _respond(self, status: int, body: Dict[str, object]) -> None:
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def _ensure_model(self) -> SoftmaxModel | None:
        with PredictorHandler.model_lock:
            if PredictorHandler.model is None:
                latest = latest_model_path()
                if latest:
                    PredictorHandler.model = SoftmaxModel.from_file(latest)
            return PredictorHandler.model


def format_prediction(label: str, confidence: float, context: Dict[str, object]) -> Dict[str, object]:
    if label == "f1":
        strategy = "edge-quic-merkle"
        latency_ms = 320
        guard = "dual-path-quic"
    elif label == "clinic":
        strategy = "multipath-erasure"
        latency_ms = 2800
        guard = "erasure-5+3"
    else:
        strategy = "adaptive-grpc"
        latency_ms = 1200
        guard = "auto-retry"
    return {
        "path": label,
        "strategy": strategy,
        "latencyMs": latency_ms,
        "confidence": confidence,
        "failureGuard": guard,
        "context": context,
    }


@app.command()
def train(  # type: ignore[override]
    dataset: pathlib.Path = typer.Option(pathlib.Path("ml-coordinator/data/training.jsonl"), help="JSONL dataset"),
    epochs: int = typer.Option(400, help="Training epochs"),
    lr: float = typer.Option(0.05, help="Learning rate"),
) -> None:
    """Train the routing model from a dataset and persist weights."""
    dataset.parent.mkdir(parents=True, exist_ok=True)
    samples = load_dataset(dataset)
    model, metrics = train_model(samples, epochs=epochs, lr=lr)
    path = persist_model(model, metrics)
    typer.echo(f"saved model to {path} (accuracy={metrics['accuracy']:.3f})")


@app.command()
def predict(  # type: ignore[override]
    size_gb: float = typer.Option(..., help="Payload size in GB"),
    criticality: float = typer.Option(..., help="Criticality score 0-1"),
    reliability: float = typer.Option(..., help="Reliability importance 0-1"),
    priority: float = typer.Option(..., help="Priority score 0-1"),
) -> None:
    """Produce a prediction using the latest trained model."""
    model_path = latest_model_path()
    if not model_path:
        raise typer.Exit("no trained model found, run `ml-coordinator train` first")
    model = SoftmaxModel.from_file(model_path)
    label, confidence = model.predict([size_gb, criticality, reliability, priority])
    payload = format_prediction(label, confidence, {})
    typer.echo(json.dumps(payload, indent=2))


@app.command()
def serve(  # type: ignore[override]
    port: int = typer.Option(8088, help="HTTP port"),
) -> None:
    """Serve predictions over HTTP for the orchestrator."""
    server = HTTPServer(("0.0.0.0", port), PredictorHandler)
    typer.echo(f"serving ML predictions on :{port}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:  # pragma: no cover - manual termination
        typer.echo("shutting down")
        server.shutdown()


if __name__ == "__main__":
    app()
