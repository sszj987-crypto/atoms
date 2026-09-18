import { FormEvent, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { RunProgressPanel } from "./RunProgressPanel";
import { SourceFilesPanel } from "./SourceFilesPanel";
import { VersionHistoryDrawer, VersionRestoreNotice, useVersionHistory, restoreIsBusy } from "./VersionHistory";
import { WorkspaceHeader } from "./WorkspaceHeader";
import { currentVersionNumber } from "./versionHistoryState";
import "./styles.css";
import "./phase2.css";
import "./phase3.css";

type User = { id: string; email: string };
type Config = { configured?: boolean; base_url?: string; api_key_set?: boolean; model?: string };
type Project = { id: string; name: string; last_accessed_at: string };
type RunProgress = { id: number; step_id?: string; kind: string; title: string; detail?: string; status?: string; created_at?: string };
type Run = { id: string; status: string; error_message?: string };
type Page = "home" | "projects" | "project" | "settings" | "auth";
type Navigate = (page: Page, projectID?: string) => void;

function NavIcon({ name }: { name: "home" | "projects" | "settings" }) {
  const paths = {
    home: <><path d="M3 10.5 12 3l9 7.5" /><path d="M5 9.5V21h14V9.5M9 21v-7h6v7" /></>,
    projects: <><path d="M3 7.5h7l2 2H21v10.5H3z" /><path d="M3 7.5V5h6l2 2.5" /></>,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V21h-4v-.1A1.7 1.7 0 0 0 8.6 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H3v-4h.1A1.7 1.7 0 0 0 4.6 8.6a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1.1V3h4v.1A1.7 1.7 0 0 0 15.4 4a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9c.12.36.33.7.6 1 .3.29.68.43 1.1.4h.1v4h-.1a1.7 1.7 0 0 0-1.7.6Z" /></>,
  } as const;
  return <svg className="nav-icon" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>;
}

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
const api = async <T,>(path: string, init?: RequestInit, timeoutMs = 30_000): Promise<T> => {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), timeoutMs);
  const abort = () => controller.abort();
  init?.signal?.addEventListener("abort", abort, { once: true });
  try {
    const response = await fetch(`/api${path}`, { ...init, signal: controller.signal, credentials: "include", headers: { "Content-Type": "application/json", ...(init?.headers || {}) } });
    if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || "REQUEST_FAILED"); }
    if (response.status === 204) return undefined as T;
    return response.json();
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw new Error("REQUEST_TIMEOUT");
    throw error;
  } finally {
    window.clearTimeout(timer);
    init?.signal?.removeEventListener("abort", abort);
  }
};

const errorText = (e: unknown) => {
  const m = e instanceof Error ? e.message : "REQUEST_FAILED";
  const zh: Record<string, string> = {
    VERSION_READ_FAILED: "读取历史版本失败",
    VERSION_SAVE_FAILED: "保存项目初始版本失败",
    VERSION_NOT_FOUND: "历史版本不存在或已清理",
    RESTORE_CREATE_FAILED: "提交版本恢复失败",
    INVALID_RESTORE: "恢复请求无效",
    RESTORE_REQUEST_CONFLICT: "恢复请求已变化，请重新选择版本",
    SOURCE_REVISION_CHANGED: "项目已发生变化，请检查最新版本后重试",
    VERSION_ALREADY_CURRENT: "该版本已经是当前版本",
    PROJECT_BUSY: "项目正在执行任务或恢复，请稍后重试",
    PROJECT_RESTORING: "项目正在恢复，完成后会自动刷新文件",
    INVALID_REGISTRATION: "注册信息无效",
    INVALID_PASSWORD: "密码无效",
    EMAIL_ALREADY_REGISTERED: "该邮箱已注册",
    INVALID_CREDENTIALS: "邮箱或密码错误",
    INVALID_LOGIN: "登录信息无效",
    AUTH_REQUIRED: "请先登录",
    PROJECT_LIMIT_REACHED: "项目数量已达上限（最多 2 个）",
    RUN_IN_PROGRESS: "有任务正在运行",
    MODEL_CONFIG_REQUIRED: "请先在设置中配置模型服务",
    MODEL_TITLE_UNAVAILABLE: "模型暂时无法生成项目名称，请稍后重试",
    INVALID_PROJECT_NAME: "项目名称需为 1–16 个字符",
    AGENT_RUN_IN_PROGRESS: "该项目已有任务正在运行",
    INVALID_MESSAGE: "请输入 1–20000 个字符的需求",
    REQUEST_TIMEOUT: "请求超时，请重试",
    TOO_MANY_ATTEMPTS: "尝试次数过多，请稍后再试",
    PROJECT_NOT_FOUND: "项目不存在或无权访问",
    PROJECT_READ_FAILED: "读取项目失败，请重试",
    INVALID_FILE_PATH: "文件路径无效或不允许访问",
    FILE_NOT_FOUND: "文件不存在，可能已被开发任务删除，请刷新文件",
    FILE_READ_FAILED: "读取文件失败，请重试",
    SOURCE_EXPORT_FAILED: "源码下载或导出失败，请重试",
    SOURCE_LIMIT_EXCEEDED: "超过文件区限制：文件列表最多 20,000 项，导出最多 10,000 个文件 / 100 MiB，单文件下载最多 100 MiB",
    RUN_READ_FAILED: "无法确认任务状态，请重试",
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
          {error && <p className="error" role="alert">{error}</p>}
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
        <span className="empty-icon" aria-hidden="true">↗</span>
        <h2>请先登录</h2>
        <p className="muted">登录后即可使用该功能，创建并预览你的项目。</p>
        <button onClick={onLogin}>登录 / 注册</button>
      </div>
    </section>
  );
}

const NAV = [["home", "首页"], ["projects", "项目"], ["settings", "设置"]] as const;

function Navigation({ page, setPage, user, onLogin, logout }: { page: Page; setPage: Navigate; user: User | null; onLogin: () => void; logout: () => void }) {
  return (
    <aside className="sidebar">
      <div className="brand">atoms</div>
      <nav aria-label="主导航">{NAV.map(([key, label]) => <a key={key} href={pathFor(key)} className={page === key ? "active" : ""} aria-current={page === key ? "page" : undefined} onClick={event => { event.preventDefault(); setPage(key); }}><NavIcon name={key} /><span>{label}</span></a>)}</nav>
      <div className="account">
        {user ? (<><span className="avatar" aria-hidden="true">{user.email.slice(0, 1).toUpperCase()}</span><span className="account-email">{user.email}</span><button className="account-action" onClick={logout}>退出</button></>) : <button onClick={onLogin}>登录 / 注册</button>}
      </div>
    </aside>
  );
}

function Home({ setPage, user, projects, onCreated, onLogin }: { setPage: Navigate; user: User | null; projects: Project[]; onCreated: (p: Project, initialDraft: string) => void; onLogin: () => void }) {
  const [request, setRequest] = useState("");
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const atLimit = projects.length >= 2;
  async function build() {
    if (creating || atLimit) return;
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
          <div className="hero-actions"><button onClick={onLogin}>开始构建</button><span>无需复杂配置流程</span></div>
          <div className="feature-list" aria-label="产品能力"><span>对话生成</span><span>隔离运行</span><span>实时预览</span></div>
        </div>
      </section>
    );
  }
  return (
    <section className="page home">
      <div className="hero">
        <p className="eyebrow">你的下一个应用</p>
        <h1>你想构建什么？</h1>
        <p className="hero-copy">从一个清晰、专注的想法开始，其余交给平台完成。</p>
        <div className="prompt-composer">
          <textarea autoFocus aria-label="描述你的应用" value={request} onChange={e => setRequest(e.target.value)} onKeyDown={e => {
            if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
            e.preventDefault();
            if (!e.repeat) void build();
          }} placeholder="例如：做一个个人财务看板，支持记录收支并查看月度趋势…" />
          {error && <p className="error" role="alert">{error}</p>}
          <div className="composer-footer">
            <p>{atLimit ? "项目总数已达到限制（最多 2 个）" : <>模型服务在 <button className="link" onClick={() => setPage("settings")}>设置</button> 中配置</>}</p>
            <button onClick={build} disabled={creating || atLimit}>{atLimit ? "已达上限" : creating ? "正在创建…" : "创建项目"}</button>
          </div>
        </div>
      </div>
    </section>
  );
}

function ProjectCard({ project, setPage, onDeleted, onRenamed }: { project: Project; setPage: Navigate; onDeleted: (id: string) => void; onRenamed: (p: Project) => void }) {
  const [deployUrl, setDeployUrl] = useState("");
  const [deploying, setDeploying] = useState(false);
  const [error, setError] = useState("");
  const [editingName, setEditingName] = useState(false);
  const [name, setName] = useState(project.name);
  useEffect(() => { if (!editingName) setName(project.name); }, [project.id, project.name, editingName]);
  async function doDeploy() {
    setDeploying(true); setError(""); setDeployUrl("");
    try { const r = await api<{ port: string }>(`/project/${project.id}/deploy`, { method: "POST" }); setDeployUrl(`http://${location.hostname}:${r.port}`); }
    catch (e) { setError(errorText(e)); }
    finally { setDeploying(false); }
  }
  async function doDelete() {
    if (!confirm(`确定删除「${project.name}」？此操作不可恢复。`)) return;
    setError("");
    try { await api(`/project/${project.id}`, { method: "DELETE" }); onDeleted(project.id); }
    catch (e) { setError(errorText(e)); }
  }
  async function rename(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const r = await api<{ project: Project }>(`/project/${project.id}`, { method: "PATCH", body: JSON.stringify({ name }) });
      onRenamed(r.project); setEditingName(false);
    } catch (e) { setError(errorText(e)); }
  }
  return (
    <div className="project-card">
      <div className="project-card-header">
        <div className="project-card-title">
        {editingName
          ? <form className="rename-form" onSubmit={rename}><input aria-label="项目名称" autoFocus maxLength={16} value={name} onChange={e => setName(e.target.value)} /><button>保存</button><button type="button" className="secondary" onClick={() => { setName(project.name); setEditingName(false); }}>取消</button></form>
          : <h2 className="editable-title" role="button" tabIndex={0} title="点击修改项目名称" onClick={() => setEditingName(true)} onKeyDown={e => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setEditingName(true); } }}>{project.name}</h2>}
        </div>
        <div className="card-actions"><button onClick={() => setPage("project", project.id)}>打开项目</button><button className="secondary" onClick={doDeploy}>部署</button><button className="danger" onClick={doDelete}>删除</button></div>
      </div>
      <div className="project-card-copy">
        <p>点击项目名称可修改，打开后继续通过对话构建。</p>
        {deploying && <p className="muted status-message" role="status">正在部署…</p>}
        {deployUrl && <p className="success status-message" role="status">已部署：<a href={deployUrl} target="_blank" rel="noreferrer">{deployUrl}</a></p>}
        {error && <p className="error status-message" role="alert">{error}</p>}
      </div>
    </div>
  );
}

function Projects({ setPage, projects, onDeleted, onRenamed }: { setPage: Navigate; projects: Project[]; onDeleted: (id: string) => void; onRenamed: (p: Project) => void }) {
  return (
    <section className="page">
      <p className="eyebrow">工作区</p>
      <h1>项目</h1>
      {projects.length
        ? projects.map(p => <ProjectCard key={p.id} project={p} setPage={setPage} onDeleted={onDeleted} onRenamed={onRenamed} />)
        : <div className="empty"><span className="empty-icon" aria-hidden="true">＋</span><h2>创建你的第一个项目</h2><p>描述一个想法，平台会准备运行环境并生成可预览的应用。</p><button onClick={() => setPage("home")}>开始创建</button></div>}
    </section>
  );
}

function ProjectWorkspace({ project, initialDraft, onDraftConsumed, onBack }: { project: Project; initialDraft: string; onDraftConsumed: () => void; onBack: () => void }) {
  const [status, setStatus] = useState("正在恢复项目环境…");
  const [reload, setReload] = useState(0);
  const [messages, setMessages] = useState<{ id: string; role: string; content: string }[]>([]);
  const [text, setText] = useState("");
  const [run, setRun] = useState<string | null>(null);
  const [progress, setProgress] = useState<RunProgress[]>([]);
  const [showProgress, setShowProgress] = useState(false);
  const [previewURL, setPreviewURL] = useState("");
  const [previewError, setPreviewError] = useState("");
  const [chatError, setChatError] = useState("");
  const [sending, setSending] = useState(false);
  const [runKnown, setRunKnown] = useState(false);
  const [outputPane, setOutputPane] = useState<"preview" | "files">("preview");
  const [mobilePane, setMobilePane] = useState<"chat" | "preview">("chat");
  const [historyOpen, setHistoryOpen] = useState(false);
  const versionHistory = useVersionHistory(project.id, reload, api, errorText, () => setReload(value => value + 1));
  const restoring = restoreIsBusy(versionHistory.history?.restore);
  const projectBusy = !versionHistory.known || !!versionHistory.history?.busy || versionHistory.submitting;
  const load = () => api<{ messages: { id: string; role: string; content: string }[] }>(`/project/${project.id}/messages`).then(r => setMessages(r.messages));
  useEffect(() => {
    setRun(null);
    setProgress([]);
    setShowProgress(false);
    setChatError("");
    setStatus("正在恢复项目环境…");
    setRunKnown(false);
    setOutputPane("preview");
    setHistoryOpen(false);
  }, [project.id]);
  useEffect(() => {
    if (run) return;
    const controller = new AbortController();
    // Recover an initial status failure and notice work started in another tab.
    const timer = window.setInterval(() => {
      api<{ run: Run | null }>(`/project/${project.id}/runs/active`, { signal: controller.signal }).then(({ run: active }) => {
        if (controller.signal.aborted) return;
        setRunKnown(true);
        if (active) { setRun(active.id); setShowProgress(true); setStatus(active.status); }
      }).catch(() => { if (!controller.signal.aborted) setRunKnown(false); });
    }, 5000);
    return () => { window.clearInterval(timer); controller.abort(); };
  }, [project.id, run]);
  useEffect(() => {
    if (!initialDraft) return;
    setText(initialDraft);
    onDraftConsumed();
  }, [initialDraft, onDraftConsumed]);
  useEffect(() => {
    let active = true;
    if (!versionHistory.known) return;
    if (restoring) { setPreviewURL(""); return; }
    setPreviewURL("");
    setPreviewError("");
    api<{ port: number }>(`/project/${project.id}/preview-access`, undefined, 120_000).then(r => {
      if (active && Number.isInteger(r.port) && r.port > 0) setPreviewURL(`${location.protocol}//${location.hostname}:${r.port}`);
    }).catch(() => {
      if (active) {
        setPreviewError("项目运行环境启动失败，请重试");
        setStatus("预览地址不可用");
      }
    });
    return () => { active = false; };
  }, [project.id, reload, restoring, versionHistory.known]);
  useEffect(() => {
    let current = true;
    load().catch(e => setChatError(errorText(e)));
    api<{ run: Run | null }>(`/project/${project.id}/runs/active`).then(({ run: active }) => {
      if (!current) return;
      setRunKnown(true);
      if (active) {
        setRun(active.id);
        setShowProgress(true);
        setStatus(active.status);
      }
    }).catch(e => {
      if (current) setChatError(errorText(e));
    });
    api<{ exists: boolean; running: boolean }>(`/project/${project.id}/runtime/status`).then(s => setStatus(s.running ? "运行时运行中" : "运行时启动中")).catch(() => setStatus("运行时不可用"));
    return () => { current = false; };
  }, [project.id]);
  useEffect(() => {
    if (!run) return;
    const e = new EventSource(`/api/project/runs/${run}/events`);
    let current = true;
    let finished = false;
    e.addEventListener("progress", v => {
      if (!current || finished) return;
      let raw: RunProgress & { content?: string };
      try { raw = JSON.parse((v as MessageEvent).data); } catch { return; }
      const next: RunProgress = { id: raw.id, step_id: raw.step_id, kind: raw.kind || "info", title: raw.title || raw.content || "收到进度更新", detail: raw.detail, status: raw.status, created_at: raw.created_at };
      setProgress(items => {
        const existing = items.findIndex(item => next.step_id ? item.step_id === next.step_id : item.id === next.id);
        if (existing >= 0) return items.map((item, index) => index === existing ? next : item);
        return [...items, next].slice(-40);
      });
    });
    function updateStatus(x: Run) {
      if (!current || finished || x.id !== run) return;
      setStatus(x.status);
      if (["COMPLETED", "FAILED", "CANCELLED"].includes(x.status)) {
        finished = true;
        e.close();
        if (x.error_message) setChatError(x.error_message);
        load().catch(error => {
          if (current) setChatError(errorText(error));
        }).finally(() => {
          if (!current) return;
          if (x.status === "COMPLETED") {
            setShowProgress(false);
            setProgress([]);
          }
          setRun(null);
          setReload(n => n + 1);
        });
      }
    }
    e.addEventListener("status", v => {
      let x: Run;
      try { x = JSON.parse((v as MessageEvent).data); } catch { return; }
      updateStatus(x);
    });
    e.onerror = () => {
      if (!current || finished) return;
      setStatus("正在重新连接工作记录…");
      // Recover a missed terminal event when the stream disconnects.
      api<Run>(`/project/runs/${run}`).then(updateStatus).catch(() => {});
    };
    return () => { current = false; e.close(); };
  }, [run, project.id]);
  async function send() {
    if (!text.trim() || run || sending || projectBusy) return;
    setSending(true);
    setChatError("");
    try {
      const r = await api<{ run_id: string }>(`/project/${project.id}/messages`, { method: "POST", body: JSON.stringify({ content: text, selected_ui: null }) });
      setText(""); setProgress([]); setShowProgress(true); load().catch(e => setChatError(errorText(e))); setRun(r.run_id);
    } catch (e) { setChatError(errorText(e)); }
    finally { setSending(false); }
  }
  function refreshPreview() {
    setReload(v => v + 1);
  }
  async function cancelRun() {
    if (!run) return;
    setChatError("");
    try {
      const result = await api<{ status: string; runtime_reset: boolean }>(`/project/runs/${run}/cancel`, { method: "POST" });
      setStatus(result.status);
      if (!result.runtime_reset) setChatError("任务正在清理，项目暂不可修改；若持续失败，请检查运行环境后重启服务。");
    } catch (e) { setChatError(errorText(e)); }
  }
  const previewAddress = previewURL || "正在准备项目地址…";
  const runtimeIssue = status.includes("不可用") || status === "FAILED";
  return (
    <section className="workspace">
      <WorkspaceHeader name={project.name} version={currentVersionNumber(versionHistory.history)} onBack={onBack}
        issue={runtimeIssue || versionHistory.history?.restore?.status === "BLOCKED"}
        status={restoring ? versionHistory.history?.restore?.status === "BLOCKED" ? "项目已保护" : "版本恢复中" : statusText(status)} />
      <VersionRestoreNotice controller={versionHistory} />
      <div className="workspace-tabs" role="tablist" aria-label="工作区面板"><button role="tab" aria-selected={mobilePane === "chat"} className={mobilePane === "chat" ? "active" : ""} onClick={() => setMobilePane("chat")}>对话</button><button role="tab" aria-selected={mobilePane === "preview"} className={mobilePane === "preview" ? "active" : ""} onClick={() => setMobilePane("preview")}>预览</button></div>
      <div className="workspace-body">
        <aside className={`chat ${mobilePane !== "chat" ? "mobile-hidden" : ""}`}>
          <div className="panel-heading">
            <div><span className="panel-kicker">对话{messages.length > 0 && ` · ${messages.length} 条消息`}</span><h2>构建记录</h2></div>
            <button type="button" className="secondary version-history-button" aria-haspopup="dialog" onClick={() => setHistoryOpen(true)}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 11a9 9 0 1 1 2.6 7.4M3 4v7h7M12 7v5l3 2" /></svg>
              历史记录
            </button>
          </div>
          <div className="history">{messages.length ? messages.map(m => <div key={m.id} className={`message ${m.role}`}><span>{m.role === "user" ? "你" : "Atoms"}</span><p>{m.content}</p></div>) : <div className="chat-empty"><span aria-hidden="true">✦</span><h3>从描述需求开始</h3><p>告诉我你想构建或修改什么，执行过程会实时显示在这里。</p></div>}</div>
          {showProgress && (run || progress.length > 0) && <RunProgressPanel><strong>工作记录</strong>{progress.length ? progress.map(item => <div className={`run-step ${item.kind} ${item.status || ""}`} key={item.step_id || item.id}><span className="run-step-icon" aria-hidden="true">{item.status === "completed" ? "✓" : item.status === "failed" ? "!" : item.status === "running" ? "" : "·"}</span><div><p>{item.title}</p>{item.detail && (item.kind === "command" ? <details><summary>查看命令</summary><code>{item.detail}</code></details> : <small>{item.detail}</small>)}</div></div>) : <div className="run-step running"><span className="run-step-icon" aria-hidden="true" /><div><p>正在排队…</p></div></div>}</RunProgressPanel>}
          {run && <div className="run-actions"><span>{statusText(status)}</span><button className="link" disabled={status === "CANCELLING"} onClick={cancelRun}>取消任务</button></div>}
          {chatError && <p className="error" role="alert">{chatError}</p>}
          <div className="chat-composer"><textarea aria-label="描述项目修改" value={text} onChange={e => setText(e.target.value)} onKeyDown={e => {
            if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
            e.preventDefault();
            if (!e.repeat) void send();
          }} placeholder="描述你想要的修改…" /><div><span>{restoring ? "版本恢复完成后可继续发送" : run ? "任务完成后可继续发送" : "描述越具体，结果越准确"}</span><button onClick={send} disabled={sending || !!run || projectBusy || !text.trim()}>发送</button></div></div>
        </aside>
        <div className={`preview ${mobilePane !== "preview" ? "mobile-hidden" : ""}`}>
          <div className="workspace-output-tabs" role="tablist" aria-label="项目内容" onKeyDown={event => {
            if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
            event.preventDefault();
            const next = event.key === "Home" ? "preview" : event.key === "End" ? "files" : outputPane === "preview" ? "files" : "preview";
            setOutputPane(next);
            document.getElementById(`workspace-${next}-tab`)?.focus();
          }}>
            <button id="workspace-preview-tab" role="tab" tabIndex={outputPane === "preview" ? 0 : -1} aria-selected={outputPane === "preview"} aria-controls="workspace-preview" className={outputPane === "preview" ? "active" : ""} onClick={() => setOutputPane("preview")}>预览</button>
            <button id="workspace-files-tab" role="tab" tabIndex={outputPane === "files" ? 0 : -1} aria-selected={outputPane === "files"} aria-controls="workspace-files" className={outputPane === "files" ? "active" : ""} onClick={() => setOutputPane("files")}>文件</button>
          </div>
          <div className="preview-content" hidden={outputPane !== "preview"} role="tabpanel" id="workspace-preview" aria-labelledby="workspace-preview-tab">
          <div className="preview-bar">
            <div className="browser-dots" aria-hidden="true"><i /><i /><i /></div>
            <div className="preview-addressbar"><span aria-hidden="true">⌕</span><input aria-label="项目预览地址" readOnly value={previewAddress} /></div>
            <div className="preview-actions">
              <button className="icon-button" disabled={restoring || !versionHistory.known} aria-label="刷新预览" title="刷新预览" onClick={refreshPreview}>↻</button>
            </div>
          </div>
          {previewURL ? <iframe key={reload} title="项目预览" src={previewURL} /> : previewError ? <div className="preview-loading" role="alert"><p>{previewError}</p><button disabled={restoring || !versionHistory.known} onClick={refreshPreview}>重新启动</button></div> : <div className="preview-loading" role="status"><span className="spinner" aria-hidden="true" /><p>{restoring ? "版本恢复完成后重新载入预览…" : "正在启动项目预览…"}</p></div>}
          </div>
          <SourceFilesPanel key={project.id} projectID={project.id} visible={outputPane === "files"} restoring={restoring} runActive={runKnown && versionHistory.known ? !!run || sending || projectBusy : null} refreshKey={reload} request={api} formatError={errorText} />
        </div>
      </div>
      <VersionHistoryDrawer projectID={project.id} open={historyOpen} onClose={() => setHistoryOpen(false)} controller={versionHistory} />
    </section>
  );
}

function Settings() {
  const [config, setConfig] = useState<Config>({});
  const [base_url, setBaseURL] = useState("");
  const [api_key, setKey] = useState("");
  const [model, setModel] = useState("");
  const [message, setMessage] = useState("");
  const [feedback, setFeedback] = useState<"idle" | "pending" | "success" | "error">("idle");
  const isConfigured = !!(config.configured || (config.api_key_set && config.base_url && config.model));
  useEffect(() => { api<Config>("/llm-config").then(c => { setConfig(c); setBaseURL(c.base_url || ""); setModel(c.model || ""); }).catch(() => undefined); }, []);
  const payload = () => ({ base_url, api_key, model });
  async function save() {
    setMessage(""); setFeedback("pending");
    try { await api("/llm-config", { method: "PUT", body: JSON.stringify(payload()) }); setKey(""); setConfig({ configured: true, base_url, model, api_key_set: true }); setMessage("配置已保存"); setFeedback("success"); }
    catch (e) { setMessage(errorText(e)); setFeedback("error"); }
  }
  async function test() {
    setMessage("正在测试连接…"); setFeedback("pending");
    try { await api<{ message: string }>("/llm-config/test", { method: "POST", body: JSON.stringify(api_key ? payload() : { base_url: "", api_key: "", model: "" }) }); setMessage("连接成功"); setFeedback("success"); }
    catch { setMessage("模型服务不可用，或不兼容所需的 Responses API。"); setFeedback("error"); }
  }
  return (
    <section className="page settings">
      <div className="page-heading"><div><p className="eyebrow">设置</p><h1>模型服务</h1><p className="muted">配置兼容 OpenAI Responses API 的模型服务。</p></div><span className={`config-badge ${isConfigured ? "configured" : ""}`}><i aria-hidden="true" />{isConfigured ? "已配置" : "未配置"}</span></div>
      <div className="settings-card">
        <div className="settings-section-heading"><h2>连接信息</h2><p>密钥会加密保存，仅用于执行你的项目请求。</p></div>
        <label>服务地址<input value={base_url} onChange={e => setBaseURL(e.target.value)} placeholder="https://api.example.com/v1" /><small>填写服务商提供的 API 基础地址</small></label>
        <label>API 密钥<input value={api_key} onChange={e => setKey(e.target.value)} type="password" placeholder={config.api_key_set ? "已保存密钥 — 输入新值可替换" : "输入 API Key"} /><small>{config.api_key_set ? "密钥已保存，留空表示继续使用当前密钥" : "密钥不会在页面中再次显示"}</small></label>
        <label>模型名称<input value={model} onChange={e => setModel(e.target.value)} placeholder="model-name" /><small>填写支持 Responses API 的模型标识</small></label>
        <div className={`settings-feedback ${feedback}`} role={feedback === "error" ? "alert" : "status"} aria-live="polite">{message || "保存前可先测试当前配置是否可用"}</div>
        <div className="actions"><button className="secondary" onClick={test} disabled={feedback === "pending"}>测试连接</button><button onClick={save} disabled={feedback === "pending"}>保存配置</button></div>
      </div>
    </section>
  );
}

function App() {
  const [user, setUser] = useState<User | null>(null);
  const [page, setPage] = useState<Page>(() => pageFromPath(location.pathname));
  const [projects, setProjects] = useState<Project[]>([]);
  const [workspaceDraft, setWorkspaceDraft] = useState("");
  const [pendingPath, setPendingPath] = useState<string | null>(null);
  const [authReady, setAuthReady] = useState(false);
  const [projectReady, setProjectReady] = useState(false);
  useEffect(() => {
    const onPopState = () => setPage(pageFromPath(location.pathname));
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  const currentProjectID = (() => {
    const m = location.pathname.match(/^\/projects\/([^/]+)$/);
    return m ? m[1] : null;
  })();
  const currentProject = currentProjectID ? projects.find(p => p.id === currentProjectID) || null : null;
  useEffect(() => {
    const titles: Record<Page, string> = { home: "首页", projects: "项目", project: currentProject?.name || "项目工作区", settings: "模型设置", auth: "登录" };
    document.title = `${titles[page]} · Atoms`;
  }, [page, currentProject?.name]);
  useEffect(() => { api<User>("/me").then(setUser).catch(() => undefined).finally(() => setAuthReady(true)); }, []);
  useEffect(() => {
    if (user) {
      setProjectReady(false);
      api<{ projects: Project[] }>("/project").then(r => setProjects(r.projects || [])).catch(() => setProjects([])).finally(() => setProjectReady(true));
    } else {
      setProjects([]);
      setProjectReady(true);
    }
  }, [user]);
  const navigate = (next: Page, projectID?: string) => {
    const target = pathFor(next, projectID);
    if (location.pathname !== target) window.history.pushState(null, "", target);
    setPage(next);
  };
  const logout = async () => { await api("/auth/logout", { method: "POST" }); setUser(null); setProjects([]); setWorkspaceDraft(""); navigate("home"); };
  const login = (u: User) => {
    const target = pendingPath || "/";
    setUser(u);
    setPendingPath(null);
    if (location.pathname !== target) window.history.pushState(null, "", target);
    setPage(pageFromPath(target));
  };
  const askLogin = () => { setPendingPath(location.pathname); navigate("auth"); };
  const immersive = page === "project" && !!user && !!currentProject;
  return (
    <div className={`shell ${immersive ? "shell-workspace" : ""}`}>
      {!immersive && <Navigation page={page} setPage={navigate} user={user} onLogin={askLogin} logout={logout} />}
      <main>
        {!authReady ? <section className="page"><p className="muted">正在加载…</p></section> : <>
          {page === "home" && <Home setPage={navigate} user={user} projects={projects} onCreated={(p, initialDraft) => { setProjects(prev => [...prev, p]); setWorkspaceDraft(initialDraft); }} onLogin={askLogin} />}
          {page === "projects" && (user ? <Projects setPage={navigate} projects={projects} onDeleted={(id) => setProjects(prev => prev.filter(p => p.id !== id))} onRenamed={(p) => setProjects(prev => prev.map(x => x.id === p.id ? p : x))} /> : <LoginPrompt onLogin={askLogin} />)}
          {page === "settings" && (user ? <Settings /> : <LoginPrompt onLogin={askLogin} />)}
          {page === "project" && (user ? (projectReady ? (currentProject ? <ProjectWorkspace key={currentProject.id} project={currentProject} initialDraft={workspaceDraft} onDraftConsumed={() => setWorkspaceDraft("")} onBack={() => navigate("projects")} /> : <LoginPrompt onLogin={askLogin} />) : <section className="page"><p className="muted">正在加载项目…</p></section>) : <LoginPrompt onLogin={askLogin} />)}
          {page === "auth" && <Auth onUser={login} />}
        </>}
      </main>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
