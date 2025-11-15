# Trackshift Admin UI

React/Vite console for monitoring orchestrator jobs, KPIs, policy decisions, and launching pilot scenarios.

## Local Development

1. Copy the env template and set values:
   ```bash
   cp .env.example .env
   # edit VITE_API_BASE, VITE_API_KEY, VITE_MFA_TOKEN, VITE_TENANT
   ```
2. Install dependencies & start the dev server:
   ```bash
   npm install
   npm run dev
   ```
3. Visit the printed URL (defaults to http://127.0.0.1:5173) with orchestrator running and reachable at `VITE_API_BASE`.

## Production Build

```bash
npm ci
npm run build
```
Artifacts will land in `dist/`; serve them behind an HTTPS proxy that injects the correct environment variables at build time.

## Container Image

A multi-stage Dockerfile is included for immutable deployments:

```bash
# build image with baked-in env vars
VITE_API_BASE=https://orch.prod \ \
  VITE_API_KEY=... \ \
  VITE_MFA_TOKEN=... \ \
  VITE_TENANT=pilot \ \
  docker build --build-arg VITE_API_BASE --build-arg VITE_API_KEY \
               --build-arg VITE_MFA_TOKEN --build-arg VITE_TENANT \
               -t ghcr.io/trackshift/ui-admin:latest .
```

Alternatively, run locally:

```bash
docker run -p 8080:8080 ghcr.io/trackshift/ui-admin:latest
```

> Note: Because Vite inlines `VITE_*` variables at build time, build separate images per environment (dev/stage/prod) or expand envs into a config JSON served at boot.

## Key Files

- `.env.example` — env vars expected during `npm run dev` / `npm run build`.
- `Dockerfile` + `docker/nginx.conf` — production-ready image for Nginx static hosting.
- `src/App.tsx` — main dashboard surfaces KPIs, jobs, policy + scenario controls.
