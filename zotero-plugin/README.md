# Kindle Notes for Zotero

Double-click a paper in Zotero, or select it and press Enter, to open its current Kindle Notes PDF. Expand the paper to open the original PDF. Papers without Kindle Notes open normally.

## Install

1. Use `zotero-plugin/kindle-notes.xpi` from the Linux release, or run `make plugin` in the repository root to build `dist/kindle-notes.xpi`.
2. In Zotero desktop **10.0.x**, open **Tools → Plugins** and drag the XPI into the window. Install on each desktop where you want this behavior. [Zotero plugin installation](https://www.zotero.org/support/plugins)

Updates are manual. Drag the new XPI into the same window. To restore Zotero’s default attachment selection, disable **Kindle Notes for Zotero**.

## How selection works

The plugin looks for stored PDF attachments whose filename or title matches `Kindle Notes - <SHA-256>.pdf`. It picks the most recently added match, using the higher local item ID to break a tie. Selecting an attachment directly always opens that attachment.

Run the plugin tests from the repository root with `node --test zotero-plugin/test/*.cjs`.
