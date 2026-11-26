const archiveName = /^Kindle Notes - [a-f0-9]{64}\.pdf$/i;
const hooks = new Map();

async function latestNotes(parent) {
  // Zotero excludes trashed children from getAttachments() by default.
  const ids = parent.getAttachments();
  if (!ids.length) return;
  const children = await Zotero.Items.getAsync(ids);
  let latest;
  for (const child of children) {
    if (child.parentItemID !== parent.id || child.deleted || !child.isStoredFileAttachment()
      || !child.isPDFAttachment()
      || !(archiveName.test(child.attachmentFilename) || archiveName.test(child.getField("title")))) {
      continue;
    }
    if (!latest || child.dateAdded > latest.dateAdded
      || (child.dateAdded === latest.dateAdded && child.id > latest.id)) {
      latest = child;
    }
  }
  return latest;
}

function onMainWindowLoad({ window }) {
  const pane = window.ZoteroPane;
  if (!pane || typeof pane.viewItems !== "function" || hooks.has(window)) return;
  const original = pane.viewItems;
  let active = true;
  async function viewItems(items, ...args) {
    if (active && items.length === 1 && items[0].isRegularItem()) {
      try {
        const notes = await latestNotes(items[0]);
        if (active && notes) items = [notes];
      } catch (error) {
        Zotero.logError(error);
      }
    }
    return original.call(this, items, ...args);
  }
  pane.viewItems = viewItems;
  hooks.set(window, () => {
    // Another extension may have wrapped our function since installation.
    active = false;
    if (pane.viewItems === viewItems) pane.viewItems = original;
  });
}

function onMainWindowUnload({ window }) {
  hooks.get(window)?.();
  hooks.delete(window);
}

function startup() {
  for (const window of Zotero.getMainWindows()) onMainWindowLoad({ window });
}

function shutdown() {
  for (const restore of hooks.values()) restore();
  hooks.clear();
}

function install() {}
function uninstall() {}
