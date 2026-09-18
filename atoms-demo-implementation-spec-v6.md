# Atoms Demo — Implementation Spec v4

> 面向 Codex 的可实施规格。  
> 目标：以尽可能低的复杂度，实现一个 **Local-first AI App Builder Demo**。  
> P0 重点是稳定跑通：**生成 → 运行 → 测试 → 预览 → 修改**。

---

# 1. 产品目标

用户注册并配置一个兼容 OpenAI Responses API 的模型服务后，可以创建一个 Web 项目，通过自然语言持续让系统开发项目，并在隔离容器中运行和预览。

核心体验：

```text
注册 / 登录
  ↓
Home
  ↓
在首页描述想构建的应用
  ↓
若尚无项目则自动创建项目
  ↓
系统自动修改代码
  ↓
测试 / Build / Verify
  ↓
进入 Project Workspace
  ↓
Preview
  ↓
继续用自然语言修改
```

一级导航固定为：

```text
Home
Projects
Settings
```

其中：

- `Home`：默认入口，核心聊天框。
- `Projects`：项目列表和项目管理入口。
- `Settings`：模型服务配置。
- `Project Workspace`：进入具体项目后的沉浸式开发页面，不属于一级导航。

额外提供：

```text
Point & Edit
```

用户可以在 Preview 中指向并选择某个 UI 元素，然后直接描述“这里要怎么改”，不要求用户知道 Sidebar、Header、Card 等专业术语。

---

# 2. 产品约束

## 2.1 P0 必须实现

- 用户注册、登录、退出。
- 当前用户信息。
- Home 首页聊天入口。
- Projects 项目列表页。
- Settings 独立模型配置页。
- 模型配置：
  - Base URL
  - API Key
  - Model
- 模型配置测试。
- 每个用户最多拥有两个项目。
- 项目源码物理目录隔离。
- 每个项目最多一个 Runtime Container。
- 每个项目绑定一个长期 Codex Session。
- 自动开发、测试、构建、修复。
- Preview。
- Follow-up 修改。
- Agent Run 状态。
- 24 小时未访问自动销毁 Runtime Container。
- Runtime 销毁后项目代码、Codex Session、项目数据库和聊天记录仍保留。
- UI Inspect / Point & Edit。
- Docker Compose 一键启动。
- README。

## 2.2 明确不做

P0 不实现：

- Multi-Agent。
- GitHub Integration。
- 云端部署。
- 团队协作。
- Billing。
- OAuth。
- SEO / Ads / Analytics。
- 完整代码 IDE。
- 多语言 Runtime。
- Redis / Kafka / Elasticsearch。
- Kubernetes。
- 任意 Docker Compose 项目。
- 支付。
- 视频处理。
- 大模型训练。
- 高资源后台任务。
- 生产级不可信多租户安全。
- 自研 Agent Loop。
- 自研 Agent Memory / Summary 系统。

---

# 3. 对外产品文案

产品侧不要暴露内部 Coding Agent 实现。

模型配置页仅显示：

```text
Base URL
API Key
Model
```

提示：

> 当前仅支持兼容 OpenAI Responses API 的模型服务。

测试失败时使用：

> 当前模型服务可能不兼容 Responses API，或模型、地址、鉴权配置有误。

禁止在普通产品 UI 和用户错误提示中出现：

```text
Codex
内部 Agent Engine
AgentAdapter
```

这些属于工程实现细节。

---

# 4. 技术栈

## 4.1 Platform

Backend：

```text
Go 1.25+
chi
pgx
PostgreSQL
Docker Engine API
SSE
```

Frontend：

```text
React
TypeScript
Vite
Tailwind CSS
```

React 构建后的静态文件由 Go Server 提供。

长期运行服务：

```text
atoms-app
postgres
```

---

## 4.2 Generated App

P0 固定：

```text
Next.js App Router
TypeScript
Tailwind CSS
shadcn/ui
Drizzle ORM
PostgreSQL
pnpm
Vitest
Testing Library
```

不允许自动切换为其他技术栈。

---

# 5. 项目复杂度限制

Generated App 默认限制：

```text
最多 5 个主要页面
最多 8 个数据库实体
单体应用
一个 PostgreSQL
无独立 Worker
无额外基础设施
无复杂实时通信
```

明显超出范围时，系统自动缩减为 MVP，并向用户说明裁剪内容。

例如：

```text
用户：
做一个完整淘宝。

系统：
当前运行环境资源有限，将实现商城 MVP：
商品列表、商品详情、搜索、购物车和简单商品管理。

暂不实现：
支付、推荐系统、物流系统和微服务。
```

不要求用户再次确认，直接执行缩减后的方案。

---

# 6. 总体架构

```text
                         Browser
                            │
                            │ :8080
                            ▼
                  ┌──────────────────┐
                  │    atoms-app     │
                  │                  │
                  │ React UI         │
                  │ HTTP API         │
                  │ Auth             │
                  │ Project Service  │
                  │ Codex Control    │
                  │ Runtime Manager  │
                  │ Private Preview│
                  │ Cleanup Worker   │
                  └──────┬─────┬────┘
                         │     │
                 Docker  │     │ PostgreSQL
                  API    │     │
                         │     ▼
                         │  ┌──────────┐
                         │  │ postgres │
                         │  └──────────┘
                         │
                         ▼
                 Docker Engine
                         │
                 Project Runtime
                    2 GiB / 2 CPU
                         │
                      Codex CLI
                         │
                    /workspace
```

内部实现固定使用 Codex CLI，不做 AgentAdapter 抽象。

---

# 7. Docker 网络

PostgreSQL 默认不向宿主机暴露 `5432`。

所有容器加入：

```text
atoms-internal
```

访问方式：

```text
atoms-app
  → postgres:5432

project-runtime
  → postgres:5432
```

Browser 不直接访问 PostgreSQL。

开发调试：

```bash
docker compose exec postgres psql ...
```

Compose 显式定义：

```yaml
networks:
  atoms-internal:
    name: atoms-internal
```

动态创建的 Runtime Container 也加入该网络。

---

# 8. Docker Compose

概念结构：

```yaml
services:
  atoms-app:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - atoms-data:/data
      - /var/run/docker.sock:/var/run/docker.sock
    networks:
      - atoms-internal
    depends_on:
      postgres:
        condition: service_healthy

  postgres:
    image: postgres:17
    volumes:
      - postgres-data:/var/lib/postgresql/data
    networks:
      - atoms-internal

networks:
  atoms-internal:
    name: atoms-internal
```

README 必须注明：

`atoms-app` 挂载 Docker Socket，因此拥有较高宿主机 Docker 权限。

这是 Local Demo 的工程取舍。

---

# 9. 数据目录

先按 User，再按 Project：

```text
/data/
└── users/
    └── <user_uuid>/
        └── projects/
            └── <project_uuid>/
                ├── workspace/
                ├── codex/
                └── logs/
```

目录用途：

```text
workspace/
→ Generated App 源码和持久化项目文件

codex/
→ 当前 Project 对应的 Codex Session / Thread 持久化数据

logs/
→ 可选保存 Codex 原始输出和运行日志
```

Runtime 中：

```text
workspace → /workspace
codex     → Codex 持久化目录
```

Runtime Container 可以删除，但上述目录必须保留。

---

# 10. 数据库模型

P0 只保留：

```text
users
llm_configs
projects
messages
agent_runs
```

不需要：

```text
conversations
agent_events
```

---

## 10.1 users

```sql
users (
    id UUID PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
)
```

---

## 10.2 llm_configs

```sql
llm_configs (
    id UUID PRIMARY KEY,
    user_id UUID UNIQUE NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    base_url TEXT NOT NULL,
    api_key_ciphertext BYTEA NOT NULL,
    model TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
)
```

API Key 加密存储。

---

## 10.3 projects

```sql
projects (
    id UUID PRIMARY KEY,
    user_id UUID UNIQUE NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    name TEXT NOT NULL,
    workspace_path TEXT NOT NULL,
    codex_state_path TEXT NOT NULL,

    last_accessed_at TIMESTAMPTZ NOT NULL,

    db_schema TEXT NOT NULL,
    db_username TEXT NOT NULL,
    db_password_ciphertext BYTEA NOT NULL,

    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
)
```

不保存：

```text
runtime_container_id
runtime_status
```

Docker 是 Runtime 状态的唯一事实源。

Container name 固定：

```text
atoms-project-{project_id}
```

需要 Runtime 状态时直接通过 Docker inspect 获取。

---

## 10.4 messages

用途：

- 展示完整项目 Chat 历史。
- 用户重新打开项目后仍能看到所有历史对话。
- 保存用户输入。
- 保存系统返回给用户的最终回复。
- 保存可选 Selected UI Context。

```sql
messages (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    role TEXT NOT NULL,
    content TEXT NOT NULL,
    selected_ui JSONB,

    created_at TIMESTAMPTZ NOT NULL
)
```

P0 一个 Project 只有一条长期 Chat，因此不需要 `conversations`。

注意：

`messages` 是产品 UI 的完整历史。

Codex 的上下文连续性由 Codex 自己的长期 Session 负责，不通过重新拼接全部 messages 实现。

---

## 10.5 agent_runs

用途：

- 判断当前是否正在执行任务。
- 防止同项目并发修改。
- Cancel。
- 记录完成 / 失败。
- Platform 重启后的任务状态修复。

```sql
agent_runs (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    status TEXT NOT NULL,
    user_message_id UUID REFERENCES messages(id),

    error_message TEXT,

    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
)
```

细粒度 Codex 输出不写数据库。

可选保存：

```text
/data/users/{uid}/projects/{pid}/logs/{run_id}.ndjson
```

---

# 11. Codex Session 设计

每个 Project 绑定一个长期 Codex Session / Thread。

原则：

```text
Project
  ↓
Persistent Codex Session
  ↓
多轮用户修改
  ↓
Codex 自己维护上下文
  ↓
上下文过长时使用 Codex 自身 compaction
```

Platform 不实现：

```text
context.md
自定义 summary
长期 memory engine
手工 recent-message 拼接策略
```

Codex Session 持久化目录必须位于：

```text
/data/users/{uid}/projects/{pid}/codex
```

不能只存在 Runtime Container 临时 filesystem 中。

24 小时 Runtime 被销毁后：

```text
workspace 保留
codex session 保留
messages 保留
project database 保留
```

下次 Runtime 恢复后继续 resume 同一个 Project 的 Codex Session。

---

# 12. Chat History 与 Codex Session 的职责

二者必须区分。

```text
messages 表
→ 用户可见完整聊天历史

Codex Session
→ Agent 工作上下文和内部 compaction
```

因此：

```text
完整聊天记录不会因为 Runtime 销毁而丢失
Codex 也不需要每次从头读取全部 messages
```

Platform 只在必要时将当前用户的新消息和 Selected UI Context 发送到现有 Codex Session。

---

# 13. Auth

API：

```text
POST /api/auth/register
POST /api/auth/login
POST /api/auth/logout
GET  /api/me
```

要求：

```text
Password: Argon2id
Session: HttpOnly Cookie
SameSite: Lax
```

每个 Project API 必须验证：

```text
project.user_id == current_user.id
```

P0 不做：

```text
邮箱验证
忘记密码
OAuth
MFA
```

---

# 14. 模型配置

API：

```text
GET  /api/llm-config
PUT  /api/llm-config
POST /api/llm-config/test
```

配置：

```json
{
  "base_url": "https://example.com/v1",
  "api_key": "...",
  "model": "..."
}
```

内部要求：

```text
Responses API compatible
```

产品 UI 不暴露底层 Coding Agent。

---

# 15. 模型测试

`Test` 不只测试 HTTP 可达。

后台通过一次极小的实际任务验证：

```text
Base URL
API Key
Model
Responses compatibility
```

成功：

```text
Connection successful
```

失败：

```text
The configured model service is unavailable or not compatible
with the required Responses API.
```

不要在用户错误中出现内部 Coding Agent 名称。

---

# 16. Codex 集成

P0 直接使用 Codex CLI。

不实现：

```text
AgentAdapter
Tool schema
Tool executor
Agent loop
Context manager
File edit protocol
Shell tool protocol
Agent summary system
```

这些能力全部由 Codex 负责。

---

## 16.1 Runtime 中安装

`atoms-runtime:latest` 包含：

```text
Node.js
pnpm
git
curl
bash
Codex CLI
Generated App build/test dependencies
```

Codex 工作目录：

```text
/workspace
```

---

## 16.2 调用方式

Backend 执行任务：

```text
docker exec
   ↓
project runtime
   ↓
resume project Codex session
   ↓
send user task
```

向 Codex 配置：

```text
Base URL
API Key
Model
Responses API
Project Instructions
Persistent Session State
```

具体 Codex CLI 参数、custom provider 配置、session resume 参数以实现时安装版本为准。

---

# 17. Project Instructions

项目 workspace 中提供：

```text
AGENTS.md
```

用于约束 Codex。

内容至少包括：

```text
- 只使用固定技术栈
- 不增加额外服务
- 控制项目复杂度
- 超出范围自动收敛成 MVP
- 优先增量修改
- 新增或修改业务逻辑时同步更新测试
- 修改后执行 typecheck / tests / build
- 不访问 /workspace 外路径
- 不启动额外 Docker 容器
```

Point & Edit 不要求源码 metadata 映射。

---

# 18. Chat → Codex

用户发送：

```text
POST /api/project/messages
```

Backend：

```text
保存 user message
   ↓
检查是否已有运行任务
   ↓
创建 agent_run
   ↓
ensureRuntime()
   ↓
resume Project Codex Session
   ↓
发送当前任务
   ↓
实时读取输出
   ↓
SSE
   ↓
Browser
```

发送给 Codex 的任务包含：

```text
当前用户请求
Selected UI（如果存在）
```

历史工作上下文由 Codex Session 自己维护。

---

# 19. Agent Run 状态

```text
PENDING
RUNNING
VERIFYING
REPAIRING
COMPLETED
FAILED
CANCELLED
```

一个项目同时最多一个活动任务。

冲突时：

```http
409 AGENT_RUN_IN_PROGRESS
```

---

# 20. Codex 输出

优先使用 Codex 支持的结构化 / JSON 输出模式。

Backend 转换成 SSE。

UI 只显示高层状态：

```text
Analyzing request...
Updating application...
Running tests...
Building application...
Fixing errors...
Preview ready.
```

不展示内部 chain-of-thought。

原始输出可选择保存到：

```text
logs/{run_id}.ndjson
```

---

# 21. 强制 Verification

Codex 返回完成后，Platform 必须独立验证。

顺序：

```text
pnpm typecheck
   ↓
pnpm test
   ↓
pnpm build
   ↓
start / restart preview
   ↓
HTTP GET /
```

所有步骤都必须成功。

---

## 21.1 测试要求

Generated App Starter Template 必须预置：

```text
Vitest
Testing Library
test helpers
database test helpers
```

固定脚本：

```json
{
  "scripts": {
    "typecheck": "tsc --noEmit",
    "test": "vitest run",
    "build": "next build"
  }
}
```

要求 Codex：

- 新增业务逻辑时写对应测试。
- 修改业务行为时更新测试。
- API / 数据逻辑必须有测试。
- 纯视觉样式调整不强制编写无意义测试。

---

## 21.2 Repair

任一步骤失败：

```text
verification error
   ↓
继续同一个 Codex Session
   ↓
Fix this test/build/runtime error
   ↓
完整重新执行：
typecheck
test
build
start
smoke
```

默认：

```text
MAX_REPAIR_ROUNDS=2
```

超过后：

```text
FAILED
```

不无限重试。

---

# 22. Runtime Container

每个项目最多一个。

固定命名：

```text
atoms-project-{project_id}
```

Runtime 是否存在：

```text
docker inspect
```

Docker 是 Runtime 状态唯一事实源。

限制：

```text
CPU:    2 cores
Memory: 2 GiB
PIDs:   256
```

挂载：

```text
host workspace:
  /data/users/{uid}/projects/{pid}/workspace

container:
  /workspace

host codex state:
  /data/users/{uid}/projects/{pid}/codex

container:
  Codex state directory
```

Runtime：

```text
non-root
不挂 Docker Socket
加入 atoms-internal
```

---

# 23. 2 GiB 约束

2 GiB 为硬限制。

因此：

- 项目规模必须小。
- Codex 与 Build/Test 串行。
- 禁止大型依赖。
- 禁止高资源任务。
- Build/Test 有 timeout。
- Agent Run 有 timeout。
- OOM 直接失败。

建议：

```text
Command timeout: 5 min
Agent run timeout: 15 min
Repair rounds: 2
```

OOM 用户提示：

> 项目超过当前运行环境资源限制，请简化项目需求。

---

# 24. 24 小时 Idle 回收

以下行为更新：

```text
last_accessed_at
```

包括：

- 打开项目。
- Preview 请求。
- 发送 Chat。
- 运行 Codex。
- Runtime 启动。

Preview 高频请求最多每 5 分钟更新一次 DB。

Cleanup Worker：

```text
每 15 分钟扫描
```

条件：

```text
last_accessed_at < now - 24h
AND
无活动 agent_run
AND
docker inspect 发现 runtime 存在
```

动作：

```text
docker rm -f atoms-project-{project_id}
```

保留：

```text
workspace
codex session
project DB
messages
project record
```

---

# 25. Runtime 恢复

用户访问已销毁项目：

```text
ensureRuntime()
   ↓
docker inspect
   ↓
不存在
   ↓
create container
   ↓
mount workspace
   ↓
mount codex state
   ↓
join atoms-internal
   ↓
pnpm install
   ↓
start app
   ↓
health check
```

UI：

```text
Restoring project environment...
```

恢复后继续原 Codex Session。

---

# 26. Generated App PostgreSQL

Platform PostgreSQL 同时承载：

```text
Platform metadata
Generated App data
```

但权限隔离。

每个项目创建：

```text
schema: p_<project_short_id>
role:   p_<project_short_id>
password: random
```

Runtime 只获得自己的：

```text
DATABASE_URL
```

不能获得 Platform DB admin credential。

保留独立 schema + role + password 设计，不为了减少代码牺牲项目级数据库隔离。

---

# 27. Project 创建

创建 Project 时：

1. 检查用户是否已有项目。
2. 创建 project UUID。
3. 创建目录：
   ```text
   /data/users/{uid}/projects/{pid}/workspace
   /data/users/{uid}/projects/{pid}/codex
   /data/users/{uid}/projects/{pid}/logs
   ```
4. 从固定 Starter Template 初始化。
5. 创建 project DB schema / role。
6. 写 Project 记录。
7. 初始化 Project Codex Session 所需目录。
8. Runtime Lazy Start。

Starter Template 必须初始即可：

```text
pnpm install
pnpm typecheck
pnpm test
pnpm build
pnpm dev
```

不要让 Codex 从空目录初始化整个 Next.js 工程。

---

# 28. Starter Template

Starter Template 尽量预置通用基础能力，以减少每个项目重复生成。

至少包含：

```text
Next.js
TypeScript
Tailwind
shadcn/ui
Drizzle
PostgreSQL connection
Base layout
Health endpoint
Vitest
Testing Library
DB test utilities
Inspector component
```

Codex 的主要工作应该集中在：

```text
业务页面
业务数据库 schema
业务逻辑
业务测试
```

---

# 29. Preview

Platform 控制面暴露宿主机端口 `8080`，鉴权预览代理使用 `PREVIEW_PORT_RANGE`。每个用户获得一个部署端口段，每个项目从中获得一个稳定且唯一的应用端口；只有显式部署后才发布 Runtime 的 `3000` 端口。

```text
Platform:
http://localhost:8080

Project Preview / New Tab (authenticated):
http://localhost:<preview_gateway_port>

Deployed application:
http://host:<project_deploy_port>
```

工作区内嵌 Preview 和“新标签页打开”使用同一个平台鉴权代理地址，保留应用完整路由、API 与 WebSocket/HMR。默认代理端口为 PREVIEW_PORT_RANGE（8081–8100），每个活跃项目独占一个代理入口；该入口需要项目级签名凭证，匿名访问返回 401。地址栏只读并隐藏初始化凭证。

开发时 Runtime 不发布项目的宿主机应用端口，点击部署后才发布长期分配的应用端口。部署状态持久化，重启、源码恢复与空闲后重建保留该状态。最小实现中预览和部署共用 Runtime 与源码，后续修改会影响已部署应用。升级后旧项目恢复为未部署并关闭旧端口，需要再次点击部署才开放。

---

# 30. UI Inspect / Point & Edit

功能只要求：

1. 用户打开 `Inspect UI`。
2. Hover 某元素时高亮。
3. 显示简单描述和尺寸。
4. 点击后把该元素设为 Selected UI。
5. 用户下一条修改请求自动携带 Selected UI Context。

例如：

```text
Selected: Main navigation

用户：
改窄一点，背景变深。
```

---

## 30.1 最小实现

Starter Template 内置 Inspector Script / Component。

使用：

```text
elementFromPoint()
getBoundingClientRect()
getComputedStyle()
window.postMessage
```

可读取：

```text
tag
role
text
class
aria-label
关键 computed style
```

不做：

```text
React AST Mapping
Source File Mapping
Source Line Mapping
DOM Explorer
DevTools
```

Codex 根据当前 workspace 自己搜索对应实现位置。

---

# 31. Selected UI Context

发送给 Codex：

```json
{
  "selected_ui": {
    "tag": "aside",
    "role": "navigation",
    "text": "Home Settings",
    "class": "...",
    "width": "256px",
    "backgroundColor": "..."
  },
  "request": "改窄一点"
}
```

任务描述要求：

> The user selected the following rendered UI element. Locate the corresponding implementation in the current project and apply the requested change with the smallest reasonable modification.

---

# 32. API

## Auth

```text
POST /api/auth/register
POST /api/auth/login
POST /api/auth/logout
GET  /api/me
```

## Model

```text
GET  /api/llm-config
PUT  /api/llm-config
POST /api/llm-config/test
```

## Project

```text
GET    /api/project
POST   /api/project
DELETE /api/project

GET  /api/project/runtime/status
POST /api/project/runtime/restart
```

Runtime status 由 Docker inspect 动态获取，不从 DB runtime_status 字段读取。

## Chat / Runs

```text
GET  /api/project/messages
POST /api/project/messages

GET  /api/project/runs/:id
POST /api/project/runs/:id/cancel
GET  /api/project/runs/:id/events
```

Events 使用 SSE。

---

# 33. 安全基线

至少实现：

- Password Argon2id。
- API Key encrypted at rest。
- Project DB password encrypted at rest。
- HttpOnly Session。
- Project ownership check。
- User / Project physical path isolation。
- Runtime non-root。
- Runtime 不挂 Docker Socket。
- Runtime 只拿 Project DB credential。
- Runtime 2 GiB。
- Runtime 2 CPU。
- PID limit。
- Command timeout。
- Run timeout。
- Secret 不输出 SSE / log。
- Managed Container label：
  ```text
  atoms.managed=true
  atoms.project_id=<uuid>
  ```

---

# 34. Startup Recovery

由于 Docker 是 Runtime 唯一事实源，不需要 DB 与 runtime 状态对账。

`atoms-app` 启动时只需要：

1. 查询 `atoms.managed=true` Containers。
2. 根据 project_id label 检查对应 Project 是否仍存在。
3. 删除无对应 Project 的 orphan container。
4. 不自动启动任何 Runtime。
5. 用户访问时 Lazy Restore。

这样避免维护：

```text
DB runtime_status
Docker runtime_status
```

双状态同步。

---

# 35. UI 页面与信息架构

一级导航：

```text
Home
Projects
Settings
```

登录后默认进入：

```text
/home
```

具体项目开发页面：

```text
/project
```

Project Workspace 不出现在一级导航中，而是从 Home 或 Projects 进入。

---

## 35.1 Login / Register

保持简单，只包含：

```text
Email
Password
Sign in / Register
```

P0 不需要营销 Landing Page。

---

## 35.2 Home

Home 是产品默认首页，也是核心聊天入口。

### 用户还没有项目

页面主体：

```text
┌──────────┬──────────────────────────────────────────┐
│ Logo     │                                          │
│          │          What do you want to build?      │
│ Home     │                                          │
│ Projects │     ┌──────────────────────────────┐     │
│ Settings │     │ Describe your app...         │     │
│          │     │                              │     │
│          │     └──────────────────────────────┘     │
│          │                           [ Build → ]     │
│          │                                          │
│ Avatar   │                                          │
└──────────┴──────────────────────────────────────────┘
```

用户提交首个需求后：

```text
创建 Project
  ↓
初始化 Workspace
  ↓
初始化 Project DB
  ↓
创建 / 恢复 Runtime
  ↓
初始化 Project Codex Session
  ↓
执行用户请求
  ↓
跳转 Project Workspace
```

用户不需要先进入 Projects 页手工创建项目。

### 用户已经有项目

Home 仍然保留聊天入口，但语义变成：

```text
What do you want to change?
```

并同时显示当前项目卡片：

```text
Current Project

┌─────────────────────────────────────┐
│ Personal Finance Tracker            │
│ Updated 10 min ago                  │
│                         Continue →  │
└─────────────────────────────────────┘
```

由于 P0 每个用户最多拥有两个项目：

- 用户输入明显属于当前项目的修改要求：直接继续当前项目。
- 用户尝试创建第三个全新项目：提示当前版本最多支持两个项目，可继续修改现有项目，或删除项目后重新创建。

Home 的核心目标是：

> 用户进入产品后可以立刻描述想构建或修改的内容，而不是先进行项目管理操作。

---

## 35.3 Projects

Projects 页面必须保留，用于承载 P0 最多两个项目的管理入口。

这样信息架构不会因为当前数量限制被做死，未来提高项目上限时无需重构导航。

页面：

```text
┌──────────┬────────────────────────────────────────┐
│ Logo     │ Projects                               │
│          │                                        │
│ Home     │ ┌────────────────────────────────────┐ │
│ Projects │ │ Personal Finance Tracker           │ │
│ Settings │ │ Updated 10 min ago                 │ │
│          │ │ Runtime: Running / Stopped         │ │
│          │ │                         [ Open → ] │ │
│          │ └────────────────────────────────────┘ │
│          │                                        │
│ Avatar   │                                        │
└──────────┴────────────────────────────────────────┘
```

当前 P0：

```text
项目数量 = 0 或 1
```

如果没有项目，可提供：

```text
[ Create Project ]
```

点击后可以：

- 跳回 Home 并聚焦聊天框；
- 或直接进入相同的创建流程。

不要单独增加复杂的 Project Creation Wizard。

如果已经有项目，不需要可用的 `New Project` 按钮。

---

## 35.4 Settings

Settings 专门用于模型服务配置。

不要把模型配置放在 Home Chat 或 Project Workspace。

页面：

```text
Settings

Model Service
────────────────────────────

Base URL
[ https://api.example.com/v1 ]

API Key
[ ••••••••••••••••• ]

Model
[ model-name ]

ⓘ 当前仅支持兼容 OpenAI Responses API 的模型服务。

[ Test Connection ]               [ Save ]
```

功能：

```text
读取配置
更新配置
测试配置
API Key mask
```

用户侧不显示内部 Coding Agent 实现信息。

---

## 35.5 Project Workspace

进入项目后使用沉浸式开发页面。

布局：

```text
┌──────────────────────────────────────────────────────┐
│ ← Projects   Project Name              Runtime ●    │
├──────────────────────┬───────────────────────────────┤
│ Chat                 │ Preview   ⟳   Inspect UI     │
│                      ├───────────────────────────────┤
│ Complete Chat        │                               │
│ History              │                               │
│                      │       Generated App           │
│ Run Status           │                               │
│                      │                               │
│                      │                               │
│ [ Ask for change ]   │                               │
└──────────────────────┴───────────────────────────────┘
```

左侧：

```text
完整聊天历史
当前任务状态
输入框
Cancel（任务运行时）
```

右侧：

```text
Preview
Refresh
Inspect UI
```

P0 不做：

```text
代码编辑器
常驻 Terminal
多 Panel IDE
```

技术日志需要时通过折叠的 `View details` 查看。

模型配置不出现在此页面。

---

## 35.6 导航行为

推荐路由：

```text
/login
/register
/home
/projects
/settings
/project
```

登录成功：

```text
→ /home
```

Home 发起首个项目：

```text
→ 创建 Project
→ 执行任务
→ /project
```

Projects 点击 Open：

```text
→ /project
```

Project Workspace 返回：

```text
← Projects
```

Settings：

```text
独立配置模型
```

用户登录后始终可以从左侧导航快速访问：

```text
Home
Projects
Settings
```


# 36. 实现阶段

## Phase 1 — Platform

实现：

```text
Docker Compose
PostgreSQL
Go Backend
React
Auth
Current User
Home
Projects
Settings
Model Config
Model Test
```

验收：

```text
注册
登录
配置模型
```

---

## Phase 2 — Project Runtime

实现：

```text
one user → up to two projects
user/project directory
starter template
project DB schema + role
runtime container
2 GiB / 2 CPU
internal network
authenticated private project preview
24h cleanup
runtime restore
```

验收：

创建项目后 Preview 可访问。

Runtime 删除后可以恢复。

---

## Phase 3 — Persistent Codex Session

实现：

```text
Codex CLI in runtime
Responses provider config
project-level persistent Codex state
session resume
Chat
messages
agent_runs
SSE
cancel
```

验收：

用户：

```text
做一个 Todo App，支持新增、完成和删除。
```

可以生成并运行。

然后继续：

```text
增加优先级。
```

能够在同一个 Codex Session 中继续修改。

Runtime 删除再恢复后：

```text
继续增加按优先级筛选。
```

仍能继续原项目上下文。

---

## Phase 4 — Verification / Repair

实现：

```text
typecheck
tests
build
start
HTTP smoke
repair
```

验收：

任何 verification 失败时，会继续同一个 Codex Session 进行修复。

成功前不会标记 Completed。

---

## Phase 5 — Point & Edit

实现：

```text
Inspect toggle
Hover highlight
Simple tooltip
Click select
postMessage
Selected UI → Task
```

验收：

```text
点击某个 UI
↓
"改窄一点"
↓
Codex 自动搜索并修改正确区域
↓
测试和 Build 通过
```

---

## Phase 6 — Delivery

实现：

```text
Error Handling
Tests
README
.env.example
Clean Install Verification
```

---

# 37. 平台自身测试

## Unit

至少覆盖：

```text
password
secret encryption
project ownership
two-project limit
agent run state
cleanup logic
```

## Integration

至少覆盖：

```text
register/login
model config
project create
runtime create
runtime destroy
idle cleanup
authenticated private project preview
cancel
verification
repair
```

CI 不依赖真实付费模型。

可通过 fake Codex command / fixture 测试控制面。

---

# 38. Generated App 测试要求

每次变更最终必须通过：

```text
pnpm typecheck
pnpm test
pnpm build
HTTP smoke
```

Generated App 的业务测试由 Codex 随功能一起维护。

不要求每次运行浏览器级 E2E。

平台自身可使用 Playwright 覆盖完整产品流程。

---

# 39. README

必须包含：

- 产品说明。
- 架构。
- Docker 前置要求。
- 启动：
  ```bash
  cp .env.example .env
  docker compose up --build
  ```
- 访问：
  ```text
  http://localhost:8080
  ```
- 模型配置说明。
- Responses API 兼容要求。
- Generated App 技术栈。
- 2 GiB 资源限制。
- 项目复杂度限制。
- 24h Runtime 规则。
- 数据目录。
- PostgreSQL 网络方式。
- Docker Socket 风险。
- 项目预览端口段与云防火墙配置说明。
- 完全清理数据方法。

普通用户文档不解释内部 Coding Agent。

开发者实现章节可以说明 Codex CLI 依赖。

---

# 40. 推荐配置

```env
APP_PORT=8080
DATABASE_URL=postgres://...
APP_MASTER_KEY=...

PROJECT_ROOT=/data/users

RUNTIME_IMAGE=atoms-runtime:latest
RUNTIME_CPU=2
RUNTIME_MEMORY_MB=2048
RUNTIME_PIDS=256

RUNTIME_IDLE_TTL_HOURS=24
RUNTIME_SWEEP_INTERVAL_MINUTES=15

RUN_TIMEOUT_MINUTES=15
COMMAND_TIMEOUT_SECONDS=300
MAX_REPAIR_ROUNDS=2
```

---


# 41. DECISIONS.md

Repository root 必须包含：

```text
DECISIONS.md
```

用途：

> 记录已经确定、实现过程中不允许擅自改变的核心架构与产品决策。

Codex 在开始实现前必须先阅读：

```text
SPEC.md
DECISIONS.md
```

如果实现过程中发现某项决策存在明显技术阻塞：

1. 不直接替换架构；
2. 记录具体阻塞原因；
3. 选择最小兼容实现；
4. 只有在确实无法实现时，才提出修改决策的建议。

`DECISIONS.md` 至少包含：

```text
- Backend = Go
- Frontend = React + Vite
- Platform DB = PostgreSQL
- Generated App = Next.js + TypeScript + Tailwind + shadcn/ui
- Generated App ORM = Drizzle
- Generated App Testing = Vitest + Testing Library
- One user = up to two projects in P0
- One project = one Runtime Container
- Runtime memory = 2 GiB
- Runtime CPU = 2 cores
- Runtime state source of truth = Docker
- Runtime container is disposable
- Workspace / Codex Session / Project DB / Messages are persistent
- Data path = /data/users/{user_id}/projects/{project_id}/...
- Project DB isolation = independent PostgreSQL schema + role + password
- Internal Coding Agent = Codex CLI
- Do not add AgentAdapter
- Do not implement custom Agent Loop
- One Project = one persistent Codex Session
- Use Codex's own context management / compaction
- Do not add custom context.md / summary / Agent Memory
- User-visible complete chat history is stored in messages
- Model service must support OpenAI Responses API
- Product UI must not expose internal Codex implementation
- Home is the default landing page after login
- Projects page supports up to two projects in P0
- Settings is the only place for model configuration
- Project Workspace = Chat + Run Status + Preview + Inspect UI
- Point & Edit does not implement source-code mapping in P0
- Generated App verification must run typecheck + tests + build + HTTP smoke
- Do not add Multi-Agent
- Do not expand P0 technology stack
```

该文件属于架构约束，而不是建议列表。


# 42. Definition of Done

必须真实跑通：

```text
Register
 ↓
Login
 ↓
Configure Responses-compatible Model in Settings
 ↓
Home
 ↓
Describe App
 ↓
Auto-create Project
 ↓
Persistent Codex Session
 ↓
Automatic Coding
 ↓
Typecheck / Tests / Build / Smoke
 ↓
Preview
 ↓
Follow-up Edit
 ↓
Preview Updated
 ↓
Runtime Destroy
 ↓
Runtime Restore
 ↓
Resume Same Project Session
 ↓
Continue Editing
 ↓
Inspect UI
 ↓
Select Element
 ↓
Natural Language Edit
 ↓
Tests Pass
 ↓
Correct UI Updated
```

同时满足：

```text
每用户 ≤ 1 Project
每 Project ≤ 1 Runtime
每 Project = 1 persistent Codex Session
Runtime ≤ 2 GiB
Runtime ≤ 2 CPU
24h idle → Runtime destroyed
Workspace / Codex State / Project DB / Messages preserved
```

---

# 43. Codex 实现要求

实现本项目时：

1. 不增加 Multi-Agent。
2. 不实现自研 Agent Loop。
3. 内部直接使用 Codex CLI。
4. 不增加 AgentAdapter 抽象。
5. 每个 Project 使用长期 Codex Session。
6. 使用 Codex 自身上下文管理和 compaction。
7. 不实现 `context.md`、自定义 summary 或 Agent Memory。
8. `messages` 只负责产品完整聊天历史。
9. Docker 是 Runtime 状态唯一事实源。
10. 不在 DB 保存 runtime_container_id / runtime_status。
11. Point & Edit 不建立源码映射。
12. 保留每 Project 独立 PostgreSQL schema + role。
13. Generated App 必须通过 typecheck / tests / build / smoke。
14. Starter Template 预置测试和通用基础设施。
15. 不扩大 Generated App 技术栈。
16. 不扩大 P0 功能。
17. 优先完成端到端闭环。
18. 每完成一个 Phase 后运行对应测试。
19. 所有 Generated App 修改和命令都在 Project Runtime 中完成。
20. 用户侧不暴露内部 Coding Agent 实现。
21. 模型服务只要求兼容 Responses API。
22. Home 必须作为登录后的默认入口和核心聊天入口。
23. Projects 页面必须保留，P0 当前最多显示两个项目。
24. 模型配置只能放在独立 Settings 页面，不放在 Home Chat 或 Project Workspace。
25. 最终从 clean volumes 完成一次全流程验证。
