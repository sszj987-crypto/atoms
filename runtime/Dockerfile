FROM node:22-alpine
RUN sed -i "s/dl-cdn.alpinelinux.org/mirrors.tencent.com/g" /etc/apk/repositories && apk add --no-cache bash curl git chromium font-noto-cjk && corepack disable && npm install -g pnpm@10.15.1 @openai/codex@0.153.4
COPY --chown=node:node internal/platform/starter/package.json internal/platform/starter/pnpm-lock.yaml /tmp/atoms-starter/
USER node
RUN cd /tmp/atoms-starter && pnpm fetch
# A mounted workspace is on another filesystem. Without an explicit store path,
# pnpm switches to /workspace/.pnpm-store and misses the image's fetched packages.
ENV npm_config_store_dir=/home/node/.local/share/pnpm/store
RUN cd /tmp/atoms-starter && pnpm install --offline --frozen-lockfile
COPY --chmod=755 runtime/entrypoint.sh /usr/local/bin/atoms-runtime-entrypoint
COPY --chmod=755 runtime/version-screenshot.sh /usr/local/bin/atoms-version-screenshot
WORKDIR /workspace
ENTRYPOINT ["atoms-runtime-entrypoint"]
