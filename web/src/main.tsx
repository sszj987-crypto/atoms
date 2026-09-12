import { FormEvent, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import "./phase2.css";
import "./phase3.css";

type User = { id: string; email: string };
type Config = { configured?: boolean; base_url?: string; api_key_set?: boolean; model?: string };
type Project = { id: string; name: string; last_accessed_at: string };
type Page = "home" | "projects" | "project" | "settings" | "auth";

function pageFromPath(pathname: string): Page {
  if (pathname === "/projects") return "projects";
  if (/^\/projects\/[^/]+$/.test(pathname)) return "project";
  if (pathname === "/settings") return "settings";
  if (pathname === "/login") return "auth";
  return "home";
}

function pathFor(page: Page, projectID?: string): string {
  if (page === "projects") return "/projects";
  if (page === "project") return projectID ? `/projects/${projectID}` : "/projects";
  if (page === "settings") return "/settings";
  if (page === "auth") return "/login";
  return "/";
}
const api = async <T,>(path: string, init?: RequestInit): Promise<T> => {
  const response = await fetch(`/api${path}`, { credentials: "include", headers: { "Content-Type": "application/json", ...(init?.headers || {}) }, ...init });
  if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || "REQUEST_FAILED"); }
  if (response.status === 204) return undefined as T;
  return response.json();
};

const errorText = (e: unknown) => {
  const m = e instanceof Error ? e.message : "REQUEST_FAILED";
  const zh: Record<string, string> = {
    INVALID_REGISTRATION: "注册信息无效",
    INVALID_PASSWORD: "密码无效",
    EMAIL_ALREADY_REGISTERED: "该邮箱已注册",
    INVALID_CREDENTIALS: "邮箱或密码错误",
    INVALID_LOGIN: "登录信息无效",
    AUTH_REQUIRED: "请先登录",
    PROJECT_LIMIT_REACHED: "每个账户只能创建一个项目",
    RUN_IN_PROGRESS: "有任务正在运行",
    MODEL_CONFIG_REQUIRED: "请先在设置中配置模型服务",
    MODEL_TITLE_UNAVAILABLE: "模型暂时无法生成项目名称，请稍后重试",
    INVALID_PROJECT_NAME: "项目名称需为 1–32 个字符",
  };
  return zh[m] || m.replaceAll("_", " ");
};

const statusText = (s: string) => {
  const zh: Record<string, string> = {
    PENDING: "排队中", RUNNING: "运行中", VERIFYING: "验证中", REPAIRING: "修复中",
    COMPLETED: "已完成", FAILED: "失败", CANCELLED: "已取消",
  };
  return zh[s] || s;
};

function Auth({ onUser }: { onUser: (u: User) => void }) {
  const [registering, setRegistering] = useState(false);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      onUser(await api<User>(`/auth/${registering ? "register" : "login"}`, { method: "POST", body: JSON.stringify({ email, password }) }));
    } catch (e) { setError(errorText(e)); }
  }
  return (
    <div className="auth-wrap">
      <section className="auth-card">
        <p className="eyebrow">ATOMS</p>
        <h1>{registering ? "创建账户" : "欢迎回来"}</h1>
        <p className="muted">通过简单的对话构建小型 Web 应用。</p>
        <form onSubmit={submit}>
          <label>邮箱<input autoComplete="email" type="email" required value={email} onChange={e => setEmail(e.target.value)} /></label>
          <label>密码<input autoComplete={registering ? "new-password" : "current-password"} type="password" minLength={8} required value={password} onChange={e => setPassword(e.target.value)} /></label>
          {error && <p className="error">{error}</p>}
          <button>{registering ? "注册" : "登录"}</button>
        </form>
        <button className="text-button" onClick={() => setRegistering(!registering)}>{registering ? "已有账户？去登录" : "没有账户？去注册"}</button>
      </section>
    </div>
  );
}

function LoginPrompt({ onLogin }: { onLogin: () => void }) {
  return (
    <section className="page">
      <div className="empty">
        <h2>请先登录</h2>
        <p className="muted">登录后即可使用该功能，创建并预览你的项目。</p>
        <button onClick={onLogin}>登录 / 注册</button>
      </div>
    </section>
  );
}

const NAV = [["home", "首页"], ["projects", "项目"], ["settings", "设置"]] as const;

function Navigation({ page, setPage, user, onLogin, logout }: { page: Page; setPage: (p: Page) => void; user: User | null; onLogin: () => void; logout: () => void }) {
  return (
    <aside>
      <div className="brand">atoms</div>
      <nav>{NAV.map(([key, label]) => <button key={key} className={page === key ? "active" : ""} onClick={() => setPage(key)}>{label}</button>)}</nav>
      <div className="account">
        {user ? (<><span>{user.email}</span><button onClick={logout}>退出登录</button></>) : <button onClick={onLogin}>登录 / 注册</button>}
      </div>
    </aside>
  );
}

function Home({ setPage, user, project, onCreated, onLogin }: { setPage: (p: Page, projectID?: string) => void; user: User | null; project: Project | null; onCreated: (p: Project, initialDraft: string) => void; onLogin: () => void }) {
  const [request, setRequest] = useState("");
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  async function build() {
    if (project) { setPage("project", project.id); return; }
    setError("");
    if (!request.trim()) { setError("请先描述你想构建的应用"); return; }
    setCreating(true);
    try {
      const r = await api<{ project: Project }>("/project", { method: "POST", body: JSON.stringify({ description: request.trim() }) });
      onCreated(r.project, request.trim()); setPage("project", r.project.id);
    } catch (e) { setError(errorText(e)); }
    finally { setCreating(false); }
  }
  if (!user) {
    return (
      <section className="page home">
        <div className="hero">
          <p className="eyebrow">构建你的下一个应用</p>
          <h1>用对话，构建 <span className="grad">Web 应用</span></h1>
          <p className="muted">描述一个专注的 Web 应用，平台会为你创建隔离的运行环境，并实时预览成果。</p>
          <button onClick={onLogin}>开始使用</button>
          <p className="hint">登录后即可创建项目、请求修改并实时预览。</p>
        </div>
      </section>
    );
  }
  return (
    <section className="page home">
      <div className="hero">
        <p className="eyebrow">你的下一个应用</p>
        <h1>{project ? "继续你的项目" : "你想构建什么？"}</h1>
        <p className="muted">{project ? `当前项目是「${project.name}」，运行时和预览已就绪。` : "描述一个专注的 Web 应用，创建它的隔离环境。"}</p>
        <textarea aria-label="描述你的应用" value={request} onChange={e => setRequest(e.target.value)} placeholder={project ? "打开工作区以请求修改。" : "描述你的应用…"} disabled={!!project} />
        {error && <p className="error">{error}</p>}
        <button onClick={build} disabled={creating}>{project ? "打开项目" : creating ? "正在生成项目名称…" : "创建项目"}</button>
        <p className="hint">{project ? "请求修改并在实时预览中查看结果。" : <>创建前请先在 <button className="link" onClick={() => setPage("settings")}>设置</button> 里配置模型服务。</>}</p>
      </div>
    </section>
  );
}

function Projects({ setPage, project, onDeleted, onRenamed }: { setPage: (p: Page, projectID?: string) => void; project: Project | null; onDeleted: () => void; onRenamed: (p: Project) => void }) {
  const [deployUrl, setDeployUrl] = useState("");
  const [deploying, setDeploying] = useState(false);
  const [error, setError] = useState("");
  const [editingName, setEditingName] = useState(false);
  const [name, setName] = useState(project?.name || "");
  useEffect(() => { if (!editingName) setName(project?.name || ""); }, [project?.id, project?.name, editingName]);
  async function doDeploy() {
    setDeploying(true); setError(""); setDeployUrl("");
    try { const r = await api<{ port: string }>("/project/deploy", { method: "POST" }); setDeployUrl(`http://${location.hostname}:${r.port}`); }
    catch (e) { setError(errorText(e)); }
    finally { setDeploying(false); }
  }
  async function doDelete() {
    if (!confirm("确定删除该项目？此操作不可恢复。")) return;
    setError("");
    try { await api("/project", { method: "DELETE" }); onDeleted(); }
    catch (e) { setError(errorText(e)); }
  }
  async function rename(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const r = await api<{ project: Project }>("/project", { method: "PATCH", body: JSON.stringify({ name }) });
      onRenamed(r.project); setEditingName(false);
    } catch (e) { setError(errorText(e)); }
  }
  return (
    <section className="page">
      <p className="eyebrow">工作区</p>
      <h1>项目</h1>
      {project
        ? <div className="project-card"><div>{editingName ? <form className="rename-form" onSubmit={rename}><input aria-label="项目名称" autoFocus maxLength={32} value={name} onChange={e => setName(e.target.value)} /><button>保存</button><button type="button" className="secondary" onClick={() => { setName(project.name); setEditingName(false); }}>取消</button></form> : <h2 className="editable-title" role="button" tabIndex={0} title="点击修改项目名称" onClick={() => setEditingName(true)} onKeyDown={e => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setEditingName(true); } }}>{project.name}</h2>}</div><div className="card-actions"><button onClick={() => setPage("project", project.id)}>打开</button><button className="secondary" onClick={doDeploy}>部署</button><button className="danger" onClick={doDelete}>删除</button></div></div>
        : <div className="empty"><h2>还没有项目</h2><p>从首页开始，描述你想构建的内容。</p></div>}
      {deploying && <p className="muted">正在部署…</p>}
      {deployUrl && <p className="success">已部署：<a href={deployUrl} target="_blank" rel="noreferrer">{deployUrl}</a></p>}
      {error && <p className="error">{error}</p>}
    </section>
  );
}

function ProjectWorkspace({ project, initialDraft, onDraftConsumed }: { project: Project; initialDraft: string; onDraftConsumed: () => void }) {
  const [status, setStatus] = useState("正在恢复项目环境…");
  const [reload, setReload] = useState(0);
  const [messages, setMessages] = useState<{ id: string; role: string; content: string }[]>([]);
  const [text, setText] = useState("");
  const [run, setRun] = useState<string | null>(null);
  const [progress, setProgress] = useState<string[]>([]);
  const [previewURL, setPreviewURL] = useState("");
  const load = () => api<{ messages: { id: string; role: string; content: string }[] }>("/project/messages").then(r => setMessages(r.messages));
  useEffect(() => {
    if (!initialDraft) return;
    setText(initialDraft);
    onDraftConsumed();
  }, [initialDraft, onDraftConsumed]);
  useEffect(() => {
    let active = true;
    api<{ port: number }>("/project/preview-access").then(r => {
      if (active && Number.isInteger(r.port) && r.port > 0) setPreviewURL(`${location.protocol}//${location.hostname}:${r.port}`);
    }).catch(() => { if (active) setStatus("预览地址不可用"); });
    return () => { active = false; };
  }, [project.id]);
  useEffect(() => {
    load();
    api<{ exists: boolean; running: boolean }>("/project/runtime/status").then(s => setStatus(s.running ? "运行时运行中" : "运行时启动中")).catch(() => setStatus("运行时不可用"));
  }, [reload]);
  useEffect(() => {
    if (!run) return;
    const e = new EventSource(`/api/project/runs/${run}/events`);
    e.addEventListener("progress", v => {
      const x = JSON.parse((v as MessageEvent).data);
      setProgress(items => [...items, x.content].slice(-30));
    });
    e.addEventListener("status", v => {
      const x = JSON.parse((v as MessageEvent).data);
      setStatus(x.status);
      if (["COMPLETED", "FAILED", "CANCELLED"].includes(x.status)) { e.close(); setRun(null); load(); setReload(n => n + 1); }
    });
    return () => e.close();
  }, [run]);
  async function send() {
    if (!text.trim() || run) return;
    const r = await api<{ run_id: string }>("/project/messages", { method: "POST", body: JSON.stringify({ content: text, selected_ui: null }) });
    setText(""); setProgress([]); load(); setRun(r.run_id);
  }
  const previewAddress = previewURL || "正在准备项目地址…";
  return (
    <section className="workspace">
      <header>
        <div><p className="eyebrow">项目工作区</p><h1>{project.name}</h1></div>
        <span className="runtime">● {statusText(status)}</span>
      </header>
      <div className="workspace-body">
        <aside className="chat">
          <div className="history">{messages.map(m => <p key={m.id} className={m.role}>{m.content}</p>)}</div>
          {run && <div className="run-progress" aria-live="polite">{progress.length ? progress.map((item, index) => <p key={`${index}-${item}`}>{item}</p>) : <p>正在排队…</p>}</div>}
          {run && <p className="muted">{statusText(status)} <button className="link" onClick={() => api(`/project/runs/${run}/cancel`, { method: "POST" })}>取消</button></p>}
          <textarea value={text} onChange={e => setText(e.target.value)} placeholder="描述你想要的修改…" />
          <button onClick={send} disabled={!!run}>发送</button>
        </aside>
        <div className="preview">
          <div className="preview-bar">
            <div className="preview-addressbar"><span aria-hidden="true">⌕</span><input aria-label="项目预览地址" readOnly value={previewAddress} /></div>
            <div className="preview-actions">
              <button className="secondary" onClick={() => setReload(v => v + 1)} disabled={!previewURL}>刷新</button>
            </div>
          </div>
          {previewURL ? <iframe key={reload} title="项目预览" src={previewURL} /> : <p className="muted">正在启动项目预览…</p>}
        </div>
      </div>
    </section>
  );
}

function Settings() {
  const [config, setConfig] = useState<Config>({});
  const [base_url, setBaseURL] = useState("");
  const [api_key, setKey] = useState("");
  const [model, setModel] = useState("");
  const [message, setMessage] = useState("");
  useEffect(() => { api<Config>("/llm-config").then(c => { setConfig(c); setBaseURL(c.base_url || ""); setModel(c.model || ""); }).catch(() => undefined); }, []);
  const payload = () => ({ base_url, api_key, model });
  async function save() {
    setMessage("");
    try { await api("/llm-config", { method: "PUT", body: JSON.stringify(payload()) }); setKey(""); setConfig({ configured: true, base_url, model, api_key_set: true }); setMessage("已保存。"); }
    catch (e) { setMessage(errorText(e)); }
  }
  async function test() {
    setMessage("正在测试连接…");
    try { const r = await api<{ message: string }>("/llm-config/test", { method: "POST", body: JSON.stringify(api_key ? payload() : { base_url: "", api_key: "", model: "" }) }); setMessage(r.message); }
    catch { setMessage("模型服务不可用，或不兼容所需的 Responses API。"); }
  }
  return (
    <section className="page settings">
      <p className="eyebrow">设置</p>
      <h1>模型服务</h1>
      <p className="muted">当前模型服务需兼容 OpenAI Responses API。</p>
      <label>Base URL<input value={base_url} onChange={e => setBaseURL(e.target.value)} placeholder="https://api.example.com/v1" /></label>
      <label>API Key<input value={api_key} onChange={e => setKey(e.target.value)} type="password" placeholder={config.api_key_set ? "已保存密钥 — 输入新值可替换" : "API Key"} /></label>
      <label>模型<input value={model} onChange={e => setModel(e.target.value)} placeholder="model-name" /></label>
      {message && <p className={message === "连接成功" || message === "已保存。" ? "success" : "error"}>{message}</p>}
      <div className="actions"><button className="secondary" onClick={test}>测试连接</button><button onClick={save}>保存</button></div>
    </section>
  );
}

function App() {
  const [user, setUser] = useState<User | null>(null);
  const [page, setPage] = useState<Page>(() => pageFromPath(location.pathname));
  const [project, setProject] = useState<Project | null>(null);
  const [workspaceDraft, setWorkspaceDraft] = useState("");
  const [pendingPath, setPendingPath] = useState<string | null>(null);
  const [authReady, setAuthReady] = useState(false);
  const [projectReady, setProjectReady] = useState(false);
  useEffect(() => {
    const onPopState = () => setPage(pageFromPath(location.pathname));
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  useEffect(() => { api<User>("/me").then(setUser).catch(() => undefined).finally(() => setAuthReady(true)); }, []);
  useEffect(() => {
    if (user) {
      setProjectReady(false);
      api<{ project: Project | null }>("/project").then(r => setProject(r.project)).catch(() => setProject(null)).finally(() => setProjectReady(true));
    } else {
      setProject(null);
      setProjectReady(true);
    }
  }, [user]);
  const navigate = (next: Page, projectID?: string) => {
    const target = pathFor(next, projectID || project?.id);
    if (location.pathname !== target) window.history.pushState(null, "", target);
    setPage(next);
  };
  const logout = async () => { await api("/auth/logout", { method: "POST" }); setUser(null); setProject(null); setWorkspaceDraft(""); navigate("home"); };
  const login = (u: User) => {
    const target = pendingPath || "/";
    setUser(u);
    setPendingPath(null);
    if (location.pathname !== target) window.history.pushState(null, "", target);
    setPage(pageFromPath(target));
  };
  const askLogin = () => { setPendingPath(location.pathname); navigate("auth"); };
  return (
    <div className="shell">
      <Navigation page={page} setPage={navigate} user={user} onLogin={askLogin} logout={logout} />
      <main>
        {!authReady ? <section className="page"><p className="muted">正在加载…</p></section> : <>
          {page === "home" && <Home setPage={navigate} user={user} project={project} onCreated={(p, initialDraft) => { setProject(p); setWorkspaceDraft(initialDraft); }} onLogin={askLogin} />}
          {page === "projects" && (user ? <Projects setPage={navigate} project={project} onDeleted={() => setProject(null)} onRenamed={setProject} /> : <LoginPrompt onLogin={askLogin} />)}
          {page === "settings" && (user ? <Settings /> : <LoginPrompt onLogin={askLogin} />)}
          {page === "project" && (user ? (projectReady ? (project ? <ProjectWorkspace project={project} initialDraft={workspaceDraft} onDraftConsumed={() => setWorkspaceDraft("")} /> : <LoginPrompt onLogin={askLogin} />) : <section className="page"><p className="muted">正在加载项目…</p></section>) : <LoginPrompt onLogin={askLogin} />)}
          {page === "auth" && <Auth onUser={login} />}
        </>}
      </main>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
