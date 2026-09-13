FROM node:22-alpine
RUN apk add --no-cache bash curl git && corepack disable && npm install -g pnpm@10.15.1 @openai/codex@0.153.4
COPY --chown=node:node internal/platform/starter/package.json internal/platform/starter/pnpm-lock.yaml /tmp/atoms-starter/
USER node
RUN cd /tmp/atoms-starter && pnpm fetch
WORKDIR /workspace
