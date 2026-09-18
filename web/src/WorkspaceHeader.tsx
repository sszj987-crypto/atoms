export function WorkspaceHeader({ name, version, status, issue, onBack }: {
  name: string;
  version?: number;
  status: string;
  issue: boolean;
  onBack: () => void;
}) {
  return <header className="workspace-header">
    <div className="workspace-title">
      <button type="button" className="back-button" onClick={onBack} aria-label="返回项目列表">←</button>
      <div className="workspace-identity">
        <p className="eyebrow">项目工作区</p>
        <div className="workspace-name-row">
          <h1 title={name}>{name}</h1>
          {version !== undefined && <span className="workspace-version" aria-label={`当前版本 v${version}`}>v{version}</span>}
        </div>
      </div>
    </div>
    <span className={`runtime ${issue ? "issue" : ""}`}><i aria-hidden="true" />{status}</span>
  </header>;
}
