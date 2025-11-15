# Orchestrator

Temporary control-plane stub that tracks job intents and states via in-memory map.

```
go run ./cmd/orchestrator
curl -X POST localhost:8090/jobs -d '{"intent":{"urgency":"CRITICAL"}}'
```

Next milestones: PG persistence, policy enforcement, eventing (NATS/Kafka), job scheduler, and decision log ingestion.