const { test } = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const vm = require("node:vm");

const plugin = readFileSync(join(__dirname, "../bootstrap.js"), "utf8");
const archive = "Kindle Notes - " + "a".repeat(64) + ".pdf";

function fixture() {
  const children = [];
  const parent = { id: 100, isRegularItem: () => true, getAttachments: () => children.map(x => x.id) };
  const calls = [];
  const pane = { viewItems(items, ...args) { calls.push({ items, args, pane: this }); return "opened"; } };
  const window = { ZoteroPane: pane };
  const errors = [];
  const Zotero = { Items: { getAsync: () => children }, getMainWindows: () => [window], logError: e => errors.push(e) };
  const ctx = vm.createContext({ Zotero });
  vm.runInContext(plugin, ctx);
  ctx.startup();
  function add(id, dateAdded, options = {}) {
    const child = { id, parentItemID: parent.id, dateAdded, attachmentFilename: archive,
      isRegularItem: () => false, isPDFAttachment: () => true, isStoredFileAttachment: () => true,
      getField: () => "", ...options };
    children.push(child);
    return child;
  }
  return { parent, window, pane, calls, ctx, Zotero, errors, add };
}

test("open latest managed PDF and preserve explicit opens and other selections", async () => {
  const { parent, window, pane, calls, ctx, add } = fixture();
  const old = add(1, "2026-01-01");
  add(2, "2026-02-01");
  const recent = add(3, "2026-02-01", { attachmentFilename: "renamed.pdf", getField: () => archive });
  add(4, "2026-03-01", { deleted: true });
  add(5, "2026-03-01", { isStoredFileAttachment: () => false });
  add(6, "2026-03-01", { attachmentFilename: "Kindle Notes - supplement.pdf" });
  add(7, "2026-03-01", { isPDFAttachment: () => false });
  add(8, "2026-03-01", { parentItemID: 999 });
  ctx.onMainWindowLoad({ window });
  const event = { shiftKey: true }, options = { noLocateOnMissing: true };
  assert.equal(await pane.viewItems([parent], event, options), "opened");
  assert.equal(calls[0].items[0], recent);
  assert.equal(calls[0].pane, pane);
  assert.deepEqual(calls[0].args, [event, options]);
  await pane.viewItems([old]);
  assert.equal(calls[1].items[0], old);
  await pane.viewItems([parent, old]);
  assert.deepEqual(calls[2].items, [parent, old]);
  ctx.onMainWindowUnload({ window });
  await pane.viewItems([parent]);
  assert.equal(calls[3].items[0], parent);
});

test("fallback to Zotero after missing items, lookup failure or disable beneath another wrapper", async () => {
  const { parent, pane, calls, ctx, Zotero, errors, add } = fixture();
  await pane.viewItems([parent]);
  add(1, "2026-01-01");
  Zotero.Items.getAsync = () => { throw new Error("lookup failed"); };
  await pane.viewItems([parent]);
  assert.equal(errors.length, 1);
  const inner = pane.viewItems;
  pane.viewItems = function (...args) { return inner.apply(this, args); };
  ctx.shutdown();
  await pane.viewItems([parent]);
  assert.equal(errors.length, 1);
  assert.equal(calls.length, 3);
  for (const call of calls) assert.equal(call.items[0], parent);
});
