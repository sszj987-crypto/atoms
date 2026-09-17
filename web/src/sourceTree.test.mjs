import assert from "node:assert/strict";
import test from "node:test";
import { filterSourceEntries } from "./sourceTree.ts";

const files = [
  { path: "app", is_dir: true, size: 0 },
  { path: "app/pages", is_dir: true, size: 0 },
  { path: "app/pages/Home.tsx", is_dir: false, size: 10 },
  { path: "app/pages/其他.tsx", is_dir: false, size: 10 },
  { path: "public", is_dir: true, size: 0 },
  { path: "public/中文 图片.png", is_dir: false, size: 3 },
  { path: "empty-dir", is_dir: true, size: 0 },
  { path: "pnpm-lock.yaml", is_dir: false, size: 10 },
];
const paths = query => filterSourceEntries(files, query).map(entry => entry.path);

test("empty search returns original entries", () => {
  assert.equal(filterSourceEntries(files, "  "), files);
});
test("search is case-insensitive and preserves every ancestor", () => {
  assert.deepEqual(paths(" HOME.TSX "), ["app", "app/pages", "app/pages/Home.tsx"]);
});
test("relative directory path matches descendants", () => {
  assert.deepEqual(paths("APP/PAGES"), files.slice(0, 4).map(entry => entry.path));
});
test("Chinese and space names work", () => {
  assert.deepEqual(paths("中文 图片"), ["public", "public/中文 图片.png"]);
});
test("root files, empty directories and no results", () => {
  assert.deepEqual(paths("lock"), ["pnpm-lock.yaml"]);
  assert.deepEqual(paths("empty-dir"), ["empty-dir"]);
  assert.deepEqual(paths("missing"), []);
});
