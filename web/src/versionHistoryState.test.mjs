import test from "node:test";
import assert from "node:assert/strict";
import { currentVersionNumber, restoreIsBusy, restoreFinished, shortVersionDescription } from "./versionHistoryState.ts";

test("workspace badge follows the restored current version, not the newest record", () => {
  const versions = [{id: "new", number: 12}, {id: "old", number: 3}];
  assert.equal(currentVersionNumber({versions, current_version_id: "old"}), 3);
  assert.equal(currentVersionNumber({versions, current_version_id: "new"}), 12);
  assert.equal(currentVersionNumber({versions, current_version_id: null}), undefined);
  assert.equal(currentVersionNumber({versions, current_version_id: "missing"}), undefined);
  assert.equal(currentVersionNumber(null), undefined);
});

const operation = (status, id = "one") => ({id, status, version_id:"version", phase:"VERIFYING"});
test("description counts Unicode characters, not UTF-16 code units", () => {
  assert.equal(shortVersionDescription("😀一二三四五六七八九十完整描述"), "😀一二三四五六七八九");
  assert.equal(shortVersionDescription(""), "项目修改");
});
test("blocked compensation remains busy", () => {
  for (const status of ["PENDING", "RUNNING", "RECOVERING", "BLOCKED"]) assert.equal(restoreIsBusy(operation(status)), true);
  for (const status of ["FAILED", "COMPLETED"]) assert.equal(restoreIsBusy(operation(status)), false);
  assert.equal(restoreIsBusy(null), false);
});
test("opening existing history does not reload Preview", () => {
  assert.equal(restoreFinished(null,operation("COMPLETED"),false),false);
  assert.equal(restoreFinished(operation("COMPLETED"),operation("COMPLETED"),false),false);
});
test("completion and compensation refresh the actual workspace once", () => {
  assert.equal(restoreFinished(operation("RUNNING"),operation("COMPLETED"),false),true);
  assert.equal(restoreFinished(operation("RECOVERING"),operation("FAILED"),false),true);
  assert.equal(restoreFinished(operation("COMPLETED"),operation("COMPLETED","two"),false),true);
  assert.equal(restoreFinished(operation("RUNNING"),operation("BLOCKED"),false),false);
});
test("lost POST response with fast completion is detected", () => {
  assert.equal(restoreFinished(null,operation("COMPLETED"),true),true);
  assert.equal(restoreFinished(null,operation("FAILED"),true),true);
  assert.equal(restoreFinished(null,operation("RUNNING"),true),false);
});
