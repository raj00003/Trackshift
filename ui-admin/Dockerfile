# Stage 1: build static assets
FROM node:20-bullseye AS build
WORKDIR /app

ARG VITE_API_BASE=http://orchestrator:8090
ARG VITE_API_KEY=local-admin-token
ARG VITE_MFA_TOKEN=000000
ARG VITE_TENANT=pilot

COPY package*.json ./
RUN npm ci

COPY . .
RUN printf "VITE_API_BASE=%s\nVITE_API_KEY=%s\nVITE_MFA_TOKEN=%s\nVITE_TENANT=%s\n" \
    "$VITE_API_BASE" "$VITE_API_KEY" "$VITE_MFA_TOKEN" "$VITE_TENANT" > .env.production
RUN npm run build

# Stage 2: serve via nginx
FROM nginx:1.27-alpine
COPY --from=build /app/dist /usr/share/nginx/html

RUN rm /etc/nginx/conf.d/default.conf
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
