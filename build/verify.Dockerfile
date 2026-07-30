ARG NODE_IMAGE=node:24.18.0-bookworm-slim
ARG GO_IMAGE=golang:1.26.5-bookworm

FROM ${NODE_IMAGE} AS node
FROM ${GO_IMAGE}

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && apt-get update \
 && apt-get install -y --no-install-recommends git make ca-certificates \
 && rm -rf /var/lib/apt/lists/*
