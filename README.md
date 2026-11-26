# Kindle Sync

Read papers from Zotero on a Kindle Scribe and save your handwritten notes back to the same Zotero item.

## Everyday use

1. Add a paper to the Zotero collection you watch, or to any of its subcollections. Kindle Sync fetches the PDF from Koofr WebDAV and emails it to your Kindle. Each paper gets its own message.
2. Write on the PDF. When you're ready to send it back, choose **Share → Google Drive → Upload** on the Scribe.
3. After the next sync, expand the paper in Zotero and open **Kindle Notes**. The annotated PDF sits beside the original. Exporting the same PDF again won't add a duplicate.

Kindle filenames combine the first author's surname, the last two digits of the year, and the paper's system or artifact name. For example, a 2026 paper by Reed introducing *Quill* becomes `reed26-quill.pdf`.

## Get started

Build the Linux server package from this checkout. Use `GOARCH=arm64` for an ARM64 server.

```sh
make dist GOARCH=amd64
```

Follow the [setup guide](docs/setup.md) to connect your accounts and test one paper. The first sync skips delivery of papers already in your collection. To send one of them, use `sync --item ITEM_KEY`. Once enabled, the timer checks for new papers and returned notes every five minutes.

To open the newest annotated PDF when you double-click a paper, install the optional [Kindle Notes plugin for Zotero](zotero-plugin/README.md).
