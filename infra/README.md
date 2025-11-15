# Infra

- `docker-compose/docker-compose.dev.yml`: local dev stack with Postgres, immudb, MinIO, Keycloak, Kafka/NATS, Prometheus, Grafana.
- `k8s/`: base Kustomize manifests plus overlays for local Kind and cloud clusters.
- `terraform/aws-eks`: sandbox cluster provisioning.

## Local Dev

```
make dev-up
kubectl apply -k infra/k8s/overlays/local
```

## Cloud Sandbox

```
cd infra/terraform/aws-eks
terraform init
terraform apply -var vpc_id=vpc-123 -var private_subnets='["subnet-1","subnet-2"]'
```