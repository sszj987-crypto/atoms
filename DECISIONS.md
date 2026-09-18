# DECISIONS.md

> This file records fixed P0 product and architecture decisions.  
> Codex must read this file together with the implementation spec before making changes.

## Product

- Home is the default page after login.
- Home is the primary natural-language build/change entry point.
- Projects page supports up to two projects per user in P0.
- Settings is the only place for model configuration.
- Project Workspace contains Chat, Run Status, Preview, and Inspect UI.
- Preview and a read-only Files panel share the workspace's right pane; Preview is the default tab. Switching tabs must retain the preview iframe and file selection.
- Files supports source viewing, single-file downloads, and source ZIP export from the same workspace used by Preview, even without a Runtime.
- Downloads and export require an idle project and snapshot files under the same project advisory lock as run creation and deletion. Dependencies, build artifacts, caches, logs, session state, secrets, and symbolic links are excluded.
- Files does not support editing. Project history supports source restore, not historical source browsing or a separate historical Preview.
- Verified successful tasks save immutable source ZIPs, manifests and optional fixed homepage screenshots before completion. New projects have an initial version; existing projects get a baseline before their first new task. Failed/cancelled tasks do not create versions.
- History is a 480px left drawer (bounded by mobile width) beside the workspace project name. Selection alone never modifies the project. Restore is the only switch/rollback action and requires confirmation.
- Keep at most 10 versions with monotonic numbers; protect the current version. Restore creates no new version; subsequent successful work branches from the restored version.
- Restore replaces actual source and rebuilds/checks Preview, not business data, current secrets or visible chat. Overwritten unversioned source does not become a history backup. Screenshots show only the saved homepage, never historical business data.
- Durable restore operations share the project database lock/busy state with tasks, cancellation cleanup, deletion, download/export, restart/deploy, Preview startup and idle cleanup. Revision checks and idempotency prevent conflicting requests.
- Restore verification/startup preserves canonical Next-generated `next-env.d.ts` bytes before full source hashing; custom declarations and all business/config source remain strictly checked. Homepage checks retry bounded transient startup failures.
- Keep the complete previous workspace temporarily for failed/interrupted restore compensation. Startup compensates unfinished replacement before enabling writes; compensation failure leaves the project protected.
- Product UI must not expose the internal Coding Agent implementation.
- Point & Edit is a P0 feature.
- Point & Edit does not implement DOM-to-source-code mapping in P0.

## Platform Stack

- Backend: Go.
- HTTP router: chi.
- Frontend: React + Vite + TypeScript + Tailwind CSS.
- Platform database: PostgreSQL.
- Platform services: `atoms-app` + `postgres`.
- Platform control-plane port: `8080`.
- Project Runtime keeps its assigned application port private until explicit deployment. `deployed` persists publication intent; Docker remains the source of Runtime status.
- Private previews use project-scoped paths under the platform port `8080`; do not publish standalone preview ports. An opaque sandbox separates generated JavaScript from platform credentials/APIs. Only the owner can activate/renew a 30-minute preview lease; logout revokes it. Direct browser document navigation is denied.
- PostgreSQL is not exposed to the host by default.
- Internal Docker network: `atoms-internal`.

## Generated App Stack

- Next.js App Router.
- TypeScript.
- Tailwind CSS.
- shadcn/ui.
- Drizzle ORM.
- PostgreSQL.
- pnpm.
- Vitest.
- Testing Library.
- Do not expand the generated application stack in P0.

## Project Model

- P0: one user can own at most two projects.
- Each user receives a reserved host-port range; every project keeps one stable, unique port within that range.
- The Projects page still uses plural project-oriented information architecture.
- One project has at most one Runtime Container.
- Project files are physically isolated by user and project.
- Preview is embedded only in the project workspace. Its scoped path preserves Next routes and HMR; a parent request/storage bridge is restricted to that exact project. Generated apps cannot use this bridge to access platform APIs.
- Preview and publication share one Runtime/source workspace. Private Next preview uses an enforced basePath and separate build directory without changing source/config. Published runtimes also start the public root-path server; only that server's port is published. Development/source restore still affect a published app. Cancellation of publication removes the public server/binding while preserving private preview.
- On upgrade, legacy always-published previews become private until explicitly deployed again.

Data layout:

```text
/data/users/{user_id}/projects/{project_id}/
├── workspace/
├── codex/
├── logs/
└── versions/  # source ZIPs, manifests, optional thumbnails; not mounted into Runtime
```

## Runtime

- Runtime Container is disposable.
- Docker is the single source of truth for Runtime existence/status.
- Do not store `runtime_container_id` or `runtime_status` in PostgreSQL.
- Container name: `atoms-project-{project_id}`.
- Runtime CPU limit: 2 cores.
- Runtime memory limit: 2 GiB.
- Runtime PID limit: 256.
- Runtime does not mount Docker Socket.
- Runtime runs as non-root.
- Runtime includes headless Chromium for fixed local homepage screenshots (1280×720, 10-second timeout, no platform credentials or user-provided URL). Screenshot failure must not fail source snapshot saving.
- Runtime joins `atoms-internal`.
- Runtime is destroyed after 24 hours without access.
- Destroying Runtime must not delete:
  - workspace;
  - Codex session state;
  - project database;
  - messages;
  - project record.

## Database Isolation

- Platform PostgreSQL also hosts generated application data.
- Each project gets its own PostgreSQL schema.
- Each project gets its own PostgreSQL role.
- Each project gets its own random database password.
- Project Runtime receives only its project-scoped `DATABASE_URL`.
- Project Runtime must never receive platform DB admin credentials.

## Coding Agent

- Internal Coding Agent: Codex CLI.
- Do not add an `AgentAdapter` abstraction in P0.
- Do not implement a custom Agent Loop.
- Do not implement custom file/shell/tool calling infrastructure.
- Each project uses one persistent Codex Session / Thread.
- Codex session state must be persisted outside the disposable Runtime filesystem.
- Resume the same project Codex Session after normal Runtime recreation. Successful source restore archives that session outside Runtime and starts a fresh one without deleting visible chat.
- Use Codex's own context management and compaction.
- Do not implement:
  - `.atoms/context.md`;
  - custom summarization;
  - custom Agent Memory.

## Chat

- Full user-visible chat history is stored in the `messages` table.
- `messages` is for product history, not for reproducing Codex internal context.
- No `conversations` table is required in P0.
- No `agent_events` table is required in P0.
- `agent_runs` is retained for run lifecycle, cancellation, failure, and concurrency control.

## Model Configuration

User-visible fields:

```text
Base URL
API Key
Model
```

- Model service must support OpenAI Responses API.
- Product UI should only communicate the Responses API compatibility requirement.
- Product UI must not mention Codex.
- The user's own provider/API quota is used.
- No OpenAI/ChatGPT interactive login is required by the product flow.

## Verification

A generated application change is not complete until all required verification passes:

```text
pnpm typecheck
pnpm test
pnpm build
start/restart preview
HTTP smoke test
```

- New or changed business logic should include/update tests.
- Pure visual style changes do not require meaningless unit tests.
- On verification failure, continue the same Codex Session and request repair.
- Maximum repair rounds: 2 by default.
- Do not mark a run successful before verification completes.

## Starter Template

The starter template is a core platform capability, not example code.

It should preconfigure:

- Next.js;
- TypeScript;
- Tailwind;
- shadcn/ui;
- Drizzle;
- PostgreSQL connection;
- base layout;
- health endpoint;
- Vitest;
- Testing Library;
- DB test utilities;
- Inspector component.

The Coding Agent should primarily implement:

- business pages;
- business database schema;
- business logic;
- business tests.

## Scope Control

Do not add in P0:

- Multi-Agent;
- GitHub integration;
- cloud deployment;
- billing;
- teams;
- OAuth;
- full IDE;
- multi-language Runtime;
- Redis;
- Kafka;
- Elasticsearch;
- Kubernetes;
- arbitrary Docker Compose projects;
- payment systems;
- video processing;
- model training;
- heavy background workers.

When a user requests an application that is too complex for the runtime/resource limits, reduce it to a reasonable MVP rather than expanding the platform architecture.
