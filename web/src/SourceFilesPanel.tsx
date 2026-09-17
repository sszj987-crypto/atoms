import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { filterSourceEntries, type SourceEntry } from "./sourceTree";
import "./files.css";

type SourceFile = SourceEntry & { previewable: boolean; reason?: string; content?: string };
type Props = {
  projectID: string;
  visible: boolean;
  runActive: boolean | null;
  refreshKey: number;
  request: <T>(path: string, init?: RequestInit, timeoutMs?: number) => Promise<T>;
  formatError: (error: unknown) => string;
};

function sizeText(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MiB`;
}

export function SourceFilesPanel({ projectID, visible, runActive, refreshKey, request, formatError }: Props) {
  const [files, setFiles] = useState<SourceEntry[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [listError, setListError] = useState("");
  const [revision, setRevision] = useState(0);
  const [listRefresh, setListRefresh] = useState(0);
  const [serverRunActive, setServerRunActive] = useState(true);
  const [selected, setSelected] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [treeVisible, setTreeVisible] = useState(true);
  const [search, setSearch] = useState("");
  const [searchCollapsed, setSearchCollapsed] = useState<Set<string>>(new Set());
  const [file, setFile] = useState<SourceFile | null>(null);
  const [fileLoading, setFileLoading] = useState(false);
  const [fileError, setFileError] = useState("");
  const [fileRefresh, setFileRefresh] = useState(0);
  const [downloadError, setDownloadError] = useState("");
  const [downloading, setDownloading] = useState<"file" | "zip" | null>(null);
  const downloadController = useRef<AbortController | null>(null);
  const downloadGeneration = useRef(0);
  const everVisible = useRef(false);
  const codeScroll = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (visible) everVisible.current = true;
    if (!everVisible.current) return;
    const controller = new AbortController();
    setLoading(true);
    setListError("");
    request<{ files: SourceEntry[]; run_active: boolean }>(`/project/${projectID}/files`, { signal: controller.signal }).then(result => {
      if (controller.signal.aborted) return;
      setFiles(result.files);
      setServerRunActive(result.run_active);
      setLoaded(true);
      setRevision(value => value + 1);
      setSelected(current => result.files.some(entry => entry.path === current && !entry.is_dir) ? current : "");
    }).catch(error => {
      if (!controller.signal.aborted) setListError(formatError(error));
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [projectID, visible, refreshKey, listRefresh, request, formatError]);

  useEffect(() => {
    setFile(null);
    setFileError("");
    setDownloadError("");
    if (!selected) { setFileLoading(false); return; }
    const controller = new AbortController();
    setFileLoading(true);
    request<SourceFile>(`/project/${projectID}/file?path=${encodeURIComponent(selected)}`, { signal: controller.signal }).then(result => {
      if (!controller.signal.aborted) setFile(result);
    }).catch(error => {
      if (!controller.signal.aborted) setFileError(formatError(error));
    }).finally(() => {
      if (!controller.signal.aborted) setFileLoading(false);
    });
    return () => controller.abort();
  }, [projectID, selected, revision, fileRefresh, request, formatError]);

  useEffect(() => {
    codeScroll.current?.scrollTo(0, 0);
  }, [selected]);

  useEffect(() => () => { downloadGeneration.current++; downloadController.current?.abort(); }, []);

  const busy = runActive !== false || serverRunActive;
  useEffect(() => {
    if (busy && downloadController.current) {
      downloadGeneration.current++;
      downloadController.current.abort();
      downloadController.current = null;
      setDownloading(null);
    }
  }, [busy]);

  const searching = search.trim().length > 0;
  const filteredFiles = useMemo(() => filterSourceEntries(files, search), [files, search]);
  const activeCollapsed = searching ? searchCollapsed : collapsed;
  const children = useMemo(() => {
    const result = new Map<string, SourceEntry[]>();
    for (const entry of filteredFiles) {
      const slash = entry.path.lastIndexOf("/");
      const parent = slash < 0 ? "" : entry.path.slice(0, slash);
      const group = result.get(parent);
      if (group) group.push(entry); else result.set(parent, [entry]);
    }
    for (const group of result.values()) group.sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.path.localeCompare(b.path));
    return result;
  }, [filteredFiles]);
  const lines = useMemo(() => file?.previewable ? (file.content || "").split("\n") : [], [file]);

  function toggleFolder(path: string) {
    const updateCollapsed = searching ? setSearchCollapsed : setCollapsed;
    updateCollapsed(previous => {
      const next = new Set(previous);
      if (next.has(path)) next.delete(path); else next.add(path);
      return next;
    });
  }

  function changeSearch(value: string) {
    setSearch(value);
    setSearchCollapsed(new Set());
  }

  function renderFolder(parent: string, depth = 0): ReactNode {
    return <ul>{(children.get(parent) || []).map(entry => <li key={entry.path}>
      <button type="button" className={`source-tree-item ${selected === entry.path ? "selected" : ""}`} style={{ paddingLeft: `${12 + depth * 14}px` }} aria-expanded={entry.is_dir ? !activeCollapsed.has(entry.path) : undefined} aria-current={!entry.is_dir && selected === entry.path ? "true" : undefined} title={entry.path} onClick={() => entry.is_dir ? toggleFolder(entry.path) : setSelected(entry.path)}>
        <span aria-hidden="true">{entry.is_dir ? activeCollapsed.has(entry.path) ? "▸" : "▾" : "◇"}</span><span>{entry.path.split("/").at(-1)}</span>
      </button>
      {entry.is_dir && !activeCollapsed.has(entry.path) && renderFolder(entry.path, depth + 1)}
    </li>)}</ul>;
  }

  async function download(kind: "file" | "zip") {
    if (busy || downloading || kind === "file" && !selected) return;
    const controller = new AbortController();
    const generation = ++downloadGeneration.current;
    downloadController.current = controller;
    const timer = window.setTimeout(() => controller.abort(), 30_000);
    setDownloading(kind);
    setDownloadError("");
    try {
      const suffix = kind === "zip" ? "export" : `file/download?path=${encodeURIComponent(selected)}`;
      const response = await fetch(`/api/project/${projectID}/${suffix}`, { credentials: "include", signal: controller.signal });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        if (body.error === "RUN_IN_PROGRESS") { setServerRunActive(true); setListRefresh(value => value + 1); }
        throw new Error(body.error || "SOURCE_EXPORT_FAILED");
      }
      const blob = await response.blob();
      if (generation !== downloadGeneration.current) return;
      const disposition = response.headers.get("Content-Disposition") || "";
      const encoded = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
      const plain = disposition.match(/filename="([^"]+)"|filename=([^;]+)/i);
      const filename = encoded ? decodeURIComponent(encoded) : plain?.[1] || plain?.[2] || (kind === "zip" ? "project-source.zip" : selected.split("/").at(-1)!);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      if (generation === downloadGeneration.current) setDownloadError(formatError(controller.signal.aborted ? new Error("REQUEST_TIMEOUT") : error));
    } finally {
      window.clearTimeout(timer);
      if (generation === downloadGeneration.current) { setDownloading(null); downloadController.current = null; }
    }
  }

  return <div className="source-panel" hidden={!visible} role="tabpanel" id="workspace-files" aria-labelledby="workspace-files-tab">
    <div className="source-toolbar">
      <div className="source-toolbar-info"><button type="button" className="secondary source-tree-toggle" aria-expanded={treeVisible} aria-controls="source-file-tree" onClick={() => setTreeVisible(value => !value)}>{treeVisible ? "收起文件树" : "展开文件树"}</button><div><span className="panel-kicker">项目源码 · 只读</span>{(runActive === null || busy) && <p role="status">{runActive === null ? "正在确认任务状态…" : "开发中，文件可能变化"}</p>}</div></div>
      <div className="source-actions">
        <button type="button" className="secondary" disabled={loading} onClick={() => setListRefresh(value => value + 1)}>刷新文件</button>
        <button type="button" className="secondary" disabled={busy || !!downloading || !selected || fileLoading || !!fileError} title={busy ? "任务完成后可下载" : "下载选中文件"} onClick={() => void download("file")}>{downloading === "file" ? "下载中…" : "下载文件"}</button>
        <button type="button" disabled={busy || !!downloading || !loaded || !!listError || loading} title={busy ? "任务完成后可导出" : "不包含依赖、构建产物或密钥"} onClick={() => void download("zip")}>{downloading === "zip" ? "导出中…" : "导出项目"}</button>
      </div>
    </div>
    {downloadError && <p className="source-error error" role="alert">{downloadError}</p>}
    {listError ? <div className="source-empty" role="alert"><p>{listError}</p><button type="button" onClick={() => setListRefresh(value => value + 1)}>重试</button></div>
      : !loaded && loading ? <div className="source-empty" role="status"><span className="spinner" aria-hidden="true" /><p>正在读取项目文件…</p></div>
      : loaded && files.length === 0 ? <div className="source-empty"><h3>暂无源码文件</h3><p>项目文件生成后会显示在这里。</p></div>
      : <div className={`source-body${treeVisible ? "" : " source-body-tree-hidden"}`} aria-busy={loading}>
        <nav className="source-tree" id="source-file-tree" hidden={!treeVisible} aria-label="项目文件">
          <div className="source-search"><input type="search" aria-label="搜索文件名或路径" placeholder="搜索文件名或路径" value={search} onChange={event => changeSearch(event.target.value)} onKeyDown={event => { if (event.key === "Escape") changeSearch(""); }} />{search && <button type="button" className="secondary" aria-label="清除搜索" title="清除搜索" onClick={() => changeSearch("")}>×</button>}</div>
          <div className="source-tree-heading" role="status">{searching ? "匹配" : "文件"}{loading ? " · 刷新中…" : ` · ${filteredFiles.filter(entry => !entry.is_dir).length}`}</div>
          {filteredFiles.length ? renderFolder("") : <p className="source-search-empty" role="status">没有匹配的文件或目录</p>}
        </nav>
        <section className="source-viewer" aria-label="文件内容">
          {selected && <div className="source-file-heading"><span title={selected}>{selected}</span>{file && <small>{sizeText(file.size)}</small>}</div>}
          {!selected ? <div className="source-empty"><h3>选择一个文件</h3><p>在左侧文件树中查看项目源码。</p></div>
            : fileLoading ? <div className="source-empty" role="status"><span className="spinner" aria-hidden="true" /><p>正在读取文件…</p></div>
            : fileError ? <div className="source-empty" role="alert"><p>{fileError}</p><button type="button" onClick={() => setFileRefresh(value => value + 1)}>重试</button></div>
            : file && !file.previewable ? <div className="source-empty"><h3>{file.reason === "too_large" ? "文件超过 1 MiB" : "二进制文件"}</h3><p>不支持在线查看，任务空闲时可下载文件。</p></div>
            : file?.previewable && <div className="source-code-scroll" ref={codeScroll} tabIndex={0} aria-label={`${selected} 只读源码`}><div className="source-code"><pre className="source-line-numbers" aria-hidden="true">{lines.map((_, index) => index + 1).join("\n")}</pre><pre><code>{file.content || ""}</code></pre></div>{file.content === undefined || file.content === "" ? <p className="source-empty-file">空文件</p> : null}</div>}
        </section>
      </div>}
    <div className="source-footer">已隐藏依赖、构建产物、缓存和敏感文件。ZIP 最多 10,000 个文件 / 100 MiB。</div>
  </div>;
}
