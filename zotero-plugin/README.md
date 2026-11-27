# Kindle Notes for Zotero

Double-click a paper in Zotero, or select it and press **Enter**, to open its latest Kindle Notes PDF. To read the original PDF, expand the paper and open that attachment. Papers without Kindle Notes open as usual.

## Install

1. Get `zotero-plugin/kindle-notes.xpi` from the extracted Kindle Sync Linux archive. If you have a source checkout, run `make plugin` in the repository root to create `dist/kindle-notes.xpi`.
2. In **Zotero 10.0.x**, open **Tools → Plugins** and drag the XPI file into the window. Repeat on each computer where you read papers. [Zotero plugin installation](https://www.zotero.org/support/plugins)

To update the plugin, drag the new XPI into the same window. To restore Zotero's default behavior, disable **Kindle Notes for Zotero** there.

## How selection works

The plugin checks the paper's stored PDF attachments for a filename or title matching `Kindle Notes - <SHA-256>.pdf`. If several match, it opens the most recently added one. The higher local item ID breaks a tie.

To test attachment selection, run `node --test zotero-plugin/test/*.cjs` from the repository root.
