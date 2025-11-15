# ML Coordinator

Python service (Typer CLI) that will aggregate metrics and push TorchScript models to edges.

```
uv run python -m app.main train --dataset data/training.json
uv run python -m app.main serve --port 8088
```

Future work: integrate Kafka/NATS consumers, Torch training loop, model registry sync, and OTA delivery to the Rust agent.