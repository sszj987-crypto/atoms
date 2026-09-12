# DECISIONS.md

> This file records fixed P0 product and architecture decisions.  
> Codex must read this file together with the implementation spec before making changes.

## Product

- Home is the default page after login.
- Home is the primary natural-language build/change entry point.
- Projects page must exist even though P0 supports only one project per user.
- Settings is the only place for model configuration.
- Project Workspace contains Chat, Run Status, Preview, and Inspect UI.
- Product UI must not expose the internal Coding Agent implementation.
- Point & Edit is a P0 feature.
- Point & Edit does not implement DOM-to-source-code mapping in P0.

## Platform Stack

- Backend: Go.
- HTTP router: chi.
- Frontend: React + Vite + TypeScript + Tailwind CSS.
- Platform database: PostgreSQL.
- Platform services: `atoms-app` + `postgres`.
- Platform external port: `8080`.
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

- P0: one user can own at most one project.
- The Projects page still uses plural project-oriented information architecture.
- One project has at most one Runtime Container.
- Project files are physically isolated by user and project.

Data layout:

```text
/data/users/{user_id}/projects/{project_id}/
├── workspace/
├── codex/
└── logs/
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
- Resume the same project Codex Session after Runtime recreation.
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
