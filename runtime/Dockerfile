FROM node:22-alpine
RUN apk add --no-cache bash curl git && corepack disable && npm install -g pnpm@10.15.1 @openai/codex@0.153.4
WORKDIR /workspace
USER node
