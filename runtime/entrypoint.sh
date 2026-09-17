#!/bin/sh
set -eu

# Only seed a fresh workspace whose dependency files match the image's template.
# Existing or customized projects still use the normal pnpm install command,
# backed by the same explicit store configured in the image.
if [ ! -e /workspace/node_modules ] &&
   cmp -s /workspace/package.json /tmp/atoms-starter/package.json &&
   cmp -s /workspace/pnpm-lock.yaml /tmp/atoms-starter/pnpm-lock.yaml; then
    echo "Reusing preinstalled starter dependencies"
    seed_dir=$(mktemp -d /workspace/.atoms-dependencies.XXXXXX)
    trap 'rm -rf "$seed_dir"' 0
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    cp -a /tmp/atoms-starter/node_modules/. "$seed_dir/"
    mv "$seed_dir" /workspace/node_modules
    trap - 0 INT HUP TERM
fi

exec "$@"
