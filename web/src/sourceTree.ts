export type SourceEntry = { path: string; is_dir: boolean; size: number };

// Keep matching entries and their ancestors so results retain their tree paths.
export function filterSourceEntries(files: SourceEntry[], query: string): SourceEntry[] {
  const term = query.trim().toLocaleLowerCase();
  if (!term) return files;
  const included = new Set<string>();
  for (const entry of files) {
    if (!entry.path.toLocaleLowerCase().includes(term)) continue;
    included.add(entry.path);
    let parent = entry.path;
    for (let slash = parent.lastIndexOf("/"); slash >= 0; slash = parent.lastIndexOf("/")) {
      parent = parent.slice(0, slash);
      included.add(parent);
    }
  }
  return files.filter(entry => included.has(entry.path));
}
