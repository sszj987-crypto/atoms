import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import "./versions.css";
import { restoreIsBusy, restoreFinished, shortVersionDescription, type RestoreState } from "./versionHistoryState";
export { restoreIsBusy } from "./versionHistoryState";

type Version = { id: string; number: number; description: string; created_at: string; has_thumbnail: boolean };
export type Restore = RestoreState;
type History = { versions: Version[]; current_version_id: string | null; source_revision: number; busy: boolean; restore: Restore | null };
type Request = <T>(path: string, init?: RequestInit, timeoutMs?: number) => Promise<T>;
const phaseText: Record<string, string> = { PREPARING: "正在校验历史版本…", STOPPING: "正在停止旧预览…", BACKING_UP: "正在准备恢复…", SWAPPING: "正在恢复项目源码…", VERIFYING: "正在安装依赖并验证项目…", SESSION_HANDOFF: "正在准备后续开发…", COMMITTING: "正在完成版本恢复…" };

function requestID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64; bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export function useVersionHistory(projectID: string, refreshKey: number, request: Request, formatError: (error: unknown) => string, onFinished: () => void) {
  const [history, setHistory] = useState<History | null>(null);
  const [known, setKnown] = useState(false);
  const [error, setError] = useState("");
  const [restoreError, setRestoreError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [showResult, setShowResult] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const previous = useRef<Restore | null>(null);
  const pending = useRef<{ version_id: string; expected_revision: number; request_id: string } | null>(null);
  const finishedCallback = useRef(onFinished);
  finishedCallback.current = onFinished;
  const generation = useRef(0);
  const pollEpoch = useRef(0);

  useEffect(() => {
    generation.current++;
    setHistory(null); setKnown(false); setError(""); setRestoreError(""); setSubmitting(false); setShowResult(false);
    previous.current = null; pending.current = null;
    return () => { generation.current++; };
  }, [projectID]);

  useEffect(() => {
    const controller = new AbortController();
    let fetching = false;
    const load = async () => {
      if (fetching) return;
      fetching = true;
      const epoch = pollEpoch.current;
      try {
        const next = await request<History>(`/project/${projectID}/versions`, { signal: controller.signal });
        if (controller.signal.aborted || epoch !== pollEpoch.current) return;
        const old = previous.current;
        // A POST may have committed even if its HTTP response was lost. A fast
        // operation can finish before the first poll observes its busy state.
        const uncertainResult = !old && pending.current && next.restore?.version_id === pending.current.version_id && next.source_revision > pending.current.expected_revision;
        if (restoreIsBusy(next.restore)) setShowResult(true);
        if (restoreFinished(old, next.restore, !!uncertainResult)) {
          setShowResult(true); finishedCallback.current();
        }
        if (uncertainResult) { pending.current = null; setRestoreError(""); }
        previous.current = next.restore;
        setHistory(next); setKnown(true); setError("");
      } catch (error) {
        if (!controller.signal.aborted && epoch === pollEpoch.current) { setKnown(false); setError(formatError(error)); }
      } finally { fetching = false; }
    };
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [projectID, refreshKey, refresh, request, formatError]);

  const resultID = history?.restore?.id;
  const resultStatus = history?.restore?.status;
  useEffect(() => {
    if (!showResult || resultStatus !== "COMPLETED") return;
    // Polling replaces history every three seconds; key this timer to the
    // operation instead so repeated responses cannot keep the notice alive.
    const timer = window.setTimeout(() => setShowResult(false), 5000);
    return () => window.clearTimeout(timer);
  }, [projectID, resultID, resultStatus, showResult]);

  async function restore(versionID: string) {
    if (!history || !known || history.busy || submitting) return false;
    const currentGeneration = generation.current;
    pollEpoch.current++;
    if (!pending.current || pending.current.version_id !== versionID || pending.current.expected_revision !== history.source_revision) pending.current = { version_id: versionID, expected_revision: history.source_revision, request_id: requestID() };
    setSubmitting(true); setRestoreError("");
    try {
      const result = await request<{ operation_id: string }>(`/project/${projectID}/restore`, { method: "POST", body: JSON.stringify(pending.current) });
      if (currentGeneration !== generation.current) return false;
      pollEpoch.current++;
      const operation: Restore = { id: result.operation_id, version_id: versionID, status: "PENDING", phase: "PREPARING" };
      previous.current = operation;
      setHistory(value => value ? { ...value, busy: true, restore: operation } : value);
      setShowResult(true); setRefresh(value => value + 1); pending.current = null;
      return true;
    } catch (error) {
      if (currentGeneration === generation.current) { setRestoreError(formatError(error)); setRefresh(value => value + 1); }
      return false;
    } finally { if (currentGeneration === generation.current) setSubmitting(false); }
  }

  return { history, known, error, restoreError, submitting, showResult, restore, refresh: () => setRefresh(value => value + 1), clearRestoreError: () => setRestoreError("") };
}
type Controller = ReturnType<typeof useVersionHistory>;

function trapDialogFocus(event: KeyboardEvent<HTMLDialogElement>) {
  if (event.key !== "Tab") return;
  const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])')).filter(item => item.getClientRects().length);
  const first = items[0], last = items[items.length - 1];
  if (!first) { event.preventDefault(); return; }
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}

export function VersionRestoreNotice({ controller }: { controller: Controller }) {
  if (controller.error) return <div className="version-notice error" role="alert"><span>{controller.error}，操作暂不可用</span><button type="button" className="secondary" onClick={controller.refresh}>重试</button></div>;
  const restore = controller.history?.restore;
  if (!restore || !controller.showResult) return null;
  const blocked = restore.status === "BLOCKED";
  return <div className={`version-notice ${blocked || restore.status === "FAILED" ? "error" : ""}`} role={blocked || restore.status === "FAILED" ? "alert" : "status"}>
    {restoreIsBusy(restore) && !blocked && <span className="spinner" aria-hidden="true" />}
    <span>{blocked || restore.status === "FAILED" ? restore.error_message : restore.status === "COMPLETED" ? "版本已恢复，源码与预览已同步更新。" : restore.status === "RECOVERING" ? "恢复未成功，正在还原原项目…" : phaseText[restore.phase] || "正在恢复版本…"}</span>
  </div>;
}

export function VersionHistoryDrawer({ projectID, open, onClose, controller }: { projectID: string; open: boolean; onClose: () => void; controller: Controller }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const confirmation = useRef<HTMLDialogElement>(null);
  const [selected, setSelected] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [failedImages, setFailedImages] = useState<Set<string>>(new Set());
  const history = controller.history;
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (open) { element.showModal(); controller.refresh(); setFailedImages(new Set()); }
    else { confirmation.current?.close(); element.close(); setConfirming(false); }
    return () => { element.close(); };
  }, [open, projectID]);
  useEffect(() => { setSelected(""); setConfirming(false); }, [projectID]);
  useEffect(() => {
    if (!history) return;
    setSelected(value => history.versions.some(version => version.id === value) ? value : history.current_version_id || history.versions[0]?.id || "");
  }, [history]);
  useEffect(() => {
    if (confirming) confirmation.current?.showModal(); else confirmation.current?.close();
  }, [confirming]);
  const version = history?.versions.find(version => version.id === selected);
  const unavailable = !controller.known || !!history?.busy || controller.submitting;
  const cancelConfirmation = () => { if (!controller.submitting) setConfirming(false); };
  const closeDrawer = () => { if (!controller.submitting) onClose(); };

  return <>
    <dialog className="version-drawer" ref={dialog} aria-labelledby="version-history-title" onKeyDown={trapDialogFocus} onCancel={event => { event.preventDefault(); closeDrawer(); }} onClick={event => {
      const bounds = event.currentTarget.getBoundingClientRect();
      if (event.target === event.currentTarget && (event.clientX > bounds.right || event.clientX < bounds.left)) closeDrawer();
    }}>
      <header className="version-drawer-header"><h2 id="version-history-title">历史记录</h2><button type="button" className="secondary" aria-label="关闭历史记录" onClick={closeDrawer} disabled={controller.submitting}>×</button></header>
      <div className="version-section-label">版本</div>
      <div className="version-list">
        {controller.error ? <div className="source-empty" role="alert"><p>{controller.error}</p><button type="button" onClick={controller.refresh}>重试</button></div>
          : !history ? <div className="source-empty" role="status"><span className="spinner" /><p>正在读取版本记录…</p></div>
          : !history.versions.length ? <div className="source-empty"><h3>暂无版本记录</h3><p>完成一次修改后，这里会保存项目版本。</p></div>
          : history.versions.map(version => <div key={version.id} className={`version-card${selected === version.id ? " selected" : ""}`}>
            <button type="button" className="version-card-select" aria-pressed={selected === version.id} title={version.description} onClick={() => setSelected(version.id)}>
              {version.has_thumbnail && !failedImages.has(version.id) ? <img src={`/api/project/${projectID}/versions/${version.id}/thumbnail`} alt={`版本 ${version.number} 首页预览`} onError={() => setFailedImages(value => new Set(value).add(version.id))} /> : <span className="version-thumbnail-missing">{version.has_thumbnail ? "预览图不可用" : "暂无预览图"}</span>}
              <span className="version-card-details"><strong>{shortVersionDescription(version.description)}</strong><time dateTime={version.created_at}>{new Date(version.created_at).toLocaleString("zh-CN", { year: "numeric", month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</time><span className="version-badges"><span>v{version.number}</span>{history.current_version_id === version.id && <span className="version-current">当前版本</span>}</span></span>
            </button>
            {selected === version.id && <div className="version-card-actions"><button type="button" className="secondary" disabled={unavailable || history.current_version_id === version.id} onClick={() => { controller.clearRestoreError(); setConfirming(true); }}>恢复到此版本</button></div>}
          </div>)}
      </div>
      {!controller.error && <VersionRestoreNotice controller={controller} />}
      {history?.busy && !restoreIsBusy(history.restore) && <p className="version-busy" role="status">任务执行或清理中，完成后可恢复版本。</p>}
      <footer className="version-drawer-footer">最多保留 10 个版本，当前版本受保护。预览图是保存时的首页截图，不包含历史业务数据。</footer>
    </dialog>
    <dialog className="version-confirm" ref={confirmation} aria-labelledby="version-confirm-title" onKeyDown={trapDialogFocus} onCancel={event => { event.preventDefault(); cancelConfirmation(); }}>
      <h2 id="version-confirm-title">恢复到版本 {version?.number}？</h2>
      <p>将替换当前项目源码并重启预览。业务数据库、密钥和聊天记录不会恢复。</p>
      <p className="version-confirm-warning">未保存的修改会被覆盖，不会生成备份版本。</p>
      {controller.restoreError && <p className="error" role="alert">{controller.restoreError}</p>}
      <div className="version-confirm-actions"><button type="button" className="secondary" disabled={controller.submitting} onClick={cancelConfirmation}>取消</button><button type="button" disabled={unavailable || !version || history?.current_version_id === version.id} onClick={async () => { if (version && await controller.restore(version.id)) setConfirming(false); }}>{controller.submitting ? "正在提交…" : "确认恢复"}</button></div>
    </dialog>
  </>;
}
