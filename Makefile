SHELL := bash

.PHONY: fmt lint test proto dev-up dev-down kind bootstrap

fmt:
	cargo fmt --manifest-path edge-agent/Cargo.toml || true
	gofmt -w $$(find gateway orchestrator -name '*.go') || true
	uv run black ml-coordinator || true
	npm --prefix ui-admin run lint:fix || true

lint:
	buf lint proto
	cargo clippy --manifest-path edge-agent/Cargo.toml --no-deps || true
	golangci-lint run ./gateway/... ./orchestrator/... || true
	uv run ruff check ml-coordinator || true
	npm --prefix ui-admin run lint
	npm --prefix ui-admin run typecheck

proto:
	cd proto && buf generate

bootstrap:
	cargo install just || true
	npm install -g pnpm || true

test:
	cargo test --manifest-path edge-agent/Cargo.toml || true
	go test ./gateway/... ./orchestrator/...
	uv run pytest ml-coordinator
	npm --prefix ui-admin test

dev-up:
	docker compose -f infra/docker-compose/docker-compose.dev.yml up -d

dev-down:
	docker compose -f infra/docker-compose/docker-compose.dev.yml down -v

kind:
	kubectl apply -k infra/k8s/overlays/local
