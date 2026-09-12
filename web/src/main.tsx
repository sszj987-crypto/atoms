import { FormEvent, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import "./phase2.css";
import "./phase3.css";

type User = { id: string; email: string };
type Config = { configured?: boolean; base_url?: string; api_key_set?: boolean; model?: string };
type Project = { id: string; name: string; last_accessed_at: string };
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

function Navigation({ page, setPage, user, onLogin, logout }: { page: string; setPage: (p: string) => void; user: User | null; onLogin: () => void; logout: () => void }) {
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

function Home({ setPage, user, project, onCreated, onLogin }: { setPage: (p: string) => void; user: User | null; project: Project | null; onCreated: (p: Project) => void; onLogin: () => void }) {
  const [request, setRequest] = useState("");
  const [error, setError] = useState("");
  async function build() {
    if (project) { setPage("project"); return; }
    setError("");
    try {
      const r = await api<{ project: Project }>("/project", { method: "POST", body: JSON.stringify({ name: request.trim() || "未命名项目" }) });
      onCreated(r.project); setPage("project");
    } catch (e) { setError(errorText(e)); }
  }
  if (!user) {
    return (
      <section className="page home">
        <div className="hero">
          <p className="eyebrow">构建你的下一个应用</p>
          <h1>用对话，构建 <span className="grad">Web 应用</span></h1>
          <p className="muted">描述一个专注的 Web 应用，平台会为你创建隔离的运行环境，并实时预览成果。</p>
          <button onClick={onLogin}>开始使用 →</button>
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
        <button onClick={build}>{project ? "打开项目 →" : "创建项目 →"}</button>
        <p className="hint">{project ? "请求修改并在实时预览中查看结果。" : <>创建前请先在 <button className="link" onClick={() => setPage("settings")}>设置</button> 里配置模型服务。</>}</p>
      </div>
    </section>
  );
}

function Projects({ setPage, project, onDeleted }: { setPage: (p: string) => void; project: Project | null; onDeleted: () => void }) {
  const [deployUrl, setDeployUrl] = useState("");
  const [deploying, setDeploying] = useState(false);
  const [error, setError] = useState("");
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
  return (
    <section className="page">
      <p className="eyebrow">工作区</p>
      <h1>项目</h1>
      {project
        ? <div className="project-card"><div><h2>{project.name}</h2><p>P0 阶段每个账户支持一个项目，访问时自动恢复运行环境。</p></div><div className="card-actions"><button onClick={() => setPage("project")}>打开 →</button><button className="secondary" onClick={doDeploy}>部署</button><button className="danger" onClick={doDelete}>删除</button></div></div>
        : <div className="empty"><h2>还没有项目</h2><p>从首页开始，描述你想构建的内容。</p></div>}
      {deploying && <p className="muted">正在部署…</p>}
      {deployUrl && <p className="success">已部署：<a href={deployUrl} target="_blank" rel="noreferrer">{deployUrl}</a></p>}
      {error && <p className="error">{error}</p>}
    </section>
  );
}

function ProjectWorkspace({ project }: { project: Project }) {
  const [status, setStatus] = useState("正在恢复项目环境…");
  const [reload, setReload] = useState(0);
  const [messages, setMessages] = useState<{ id: string; role: string; content: string }[]>([]);
  const [text, setText] = useState("");
  const [run, setRun] = useState<string | null>(null);
  const [inspect, setInspect] = useState(false);
  const [selected, setSelected] = useState<unknown>(null);
  const [token, setToken] = useState("");
  const load = () => api<{ messages: { id: string; role: string; content: string }[] }>("/project/messages").then(r => setMessages(r.messages));
  useEffect(() => {
    const h = (e: MessageEvent) => { if (e.data?.type === "atoms-selected-ui") { setSelected(e.data.selected_ui); setInspect(false); } };
    window.addEventListener("message", h);
    return () => window.removeEventListener("message", h);
  }, []);
  useEffect(() => { api<{ preview_token: string }>("/project/preview-access").then(r => setToken(r.preview_token)).catch(() => undefined); }, []);
  useEffect(() => {
    load();
    api<{ exists: boolean; running: boolean }>("/project/runtime/status").then(s => setStatus(s.running ? "运行时运行中" : "运行时启动中")).catch(() => setStatus("运行时不可用"));
  }, [reload]);
  useEffect(() => {
    if (!run) return;
    const e = new EventSource(`/api/project/runs/${run}/events`);
    e.addEventListener("status", v => {
      const x = JSON.parse((v as MessageEvent).data);
      setStatus(x.status);
      if (["COMPLETED", "FAILED", "CANCELLED"].includes(x.status)) { e.close(); setRun(null); load(); setReload(n => n + 1); }
    });
    return () => e.close();
  }, [run]);
  async function send() {
    if (!text.trim() || run) return;
    const r = await api<{ run_id: string }>("/project/messages", { method: "POST", body: JSON.stringify({ content: text, selected_ui: selected }) });
    setText(""); setSelected(null); load(); setRun(r.run_id);
  }
  const preview = `http://p-${project.id}.localhost:8080/?preview_token=${token}&preview=${reload}`;
  return (
    <section className="workspace">
      <header>
        <div><p className="eyebrow">项目工作区</p><h1>{project.name}</h1></div>
        <span className="runtime">● {statusText(status)}</span>
      </header>
      <div className="workspace-body">
        <aside className="chat">
          <div className="history">{messages.map(m => <p key={m.id} className={m.role}>{m.content}</p>)}</div>
          {Boolean(selected) && <p className="muted">已选中界面元素，准备就绪</p>}
          {run && <p className="muted">{statusText(status)} <button className="link" onClick={() => api(`/project/runs/${run}/cancel`, { method: "POST" })}>取消</button></p>}
          <textarea value={text} onChange={e => setText(e.target.value)} placeholder="描述你想要的修改…" />
          <button onClick={send} disabled={!!run}>发送</button>
        </aside>
        <div className="preview">
          <div className="preview-bar">
            <strong>预览</strong>
            <div>
              <button className="secondary" onClick={() => setReload(v => v + 1)}>刷新</button>
              <button className="secondary" onClick={() => { setInspect(v => !v); document.querySelector<HTMLIFrameElement>('iframe[title="Project preview"]')?.contentWindow?.postMessage({ type: "atoms-inspect", enabled: !inspect }, "*"); }}>检查界面</button>
            </div>
          </div>
          {token ? <iframe key={reload} title="Project preview" src={preview} /> : <p className="muted">正在加载预览…</p>}
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
  const [page, setPage] = useState("home");
  const [project, setProject] = useState<Project | null>(null);
  const [pendingPage, setPendingPage] = useState<string | null>(null);
  useEffect(() => { api<User>("/me").then(setUser).catch(() => undefined); }, []);
  useEffect(() => {
    if (user) api<{ project: Project | null }>("/project").then(r => setProject(r.project)).catch(() => undefined);
    else setProject(null);
  }, [user]);
  const logout = async () => { await api("/auth/logout", { method: "POST" }); setUser(null); setProject(null); setPage("home"); };
  const login = (u: User) => { setUser(u); setPage(pendingPage || "home"); setPendingPage(null); };
  const askLogin = () => { setPendingPage(page); setPage("auth"); };
  return (
    <div className="shell">
      <Navigation page={page} setPage={setPage} user={user} onLogin={askLogin} logout={logout} />
      <main>
        {page === "home" && <Home setPage={setPage} user={user} project={project} onCreated={setProject} onLogin={askLogin} />}
        {page === "projects" && (user ? <Projects setPage={setPage} project={project} onDeleted={() => setProject(null)} /> : <LoginPrompt onLogin={askLogin} />)}
        {page === "settings" && (user ? <Settings /> : <LoginPrompt onLogin={askLogin} />)}
        {page === "project" && (user && project ? <ProjectWorkspace project={project} /> : <LoginPrompt onLogin={askLogin} />)}
        {page === "auth" && <Auth onUser={login} />}
      </main>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
