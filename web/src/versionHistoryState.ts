export type RestoreState = { id: string; version_id: string; status: string; phase: string; error_message?: string };
export function currentVersionNumber(history: { current_version_id: string | null; versions: { id: string; number: number }[] } | null) {
  return history?.versions.find(version => version.id === history.current_version_id)?.number;
}
export const restoreIsBusy = (restore: RestoreState | null | undefined) => !!restore && ["PENDING", "RUNNING", "RECOVERING", "BLOCKED"].includes(restore.status);
export const shortVersionDescription = (description: string) => Array.from(description).slice(0, 10).join("") || "项目修改";

export function restoreFinished(previous: RestoreState | null, next: RestoreState | null, uncertainResult: boolean) {
  if (!next || restoreIsBusy(next)) return false;
  return uncertainResult || !!previous && (previous.id !== next.id || restoreIsBusy(previous));
}
