# Kindle Sync

Read papers from Zotero on a Kindle Scribe and save your handwritten notes back to the same Zotero item.

## Everyday use

1. Add a paper to the Zotero collection you watch, or to any of its subcollections. Kindle Sync fetches the PDF from Koofr WebDAV and emails it to your Kindle. Each paper gets its own message.
2. Write on the PDF. When you're ready to send it back, choose **Share → Google Drive → Upload** on the Scribe.
3. After the next sync, open **Kindle Notes** under the paper in Zotero. Each paper keeps its latest annotated PDF. When a newer export changes the PDF, Kindle Sync verifies the new copy, then moves its previous copy to Zotero Trash. The original paper and your Drive exports stay in place.

You can move a paper out of your reading collection after sending it. Notes still return to the same Zotero item. If you trash or delete the item, or an export doesn't match a paper Kindle Sync sent, the PDF stays in Drive.

Kindle filenames combine the first author's surname, the last two digits of the year, and the paper's system or artifact name. For example, a 2026 paper by Reed introducing *Quill* becomes `reed26-quill.pdf`.

## Get started

Open this checkout in Codex and ask **“Deploy Kindle Sync”**. The repository's `$kindle-sync-deploy` skill sets up Docker Compose and verifies two scheduled rounds. The [Docker guide](docs/docker.md) also covers moving an existing queue. For a native Linux service, use the [systemd guide](docs/setup.md).

Ask **“Sync my Kindle”** for a two-way sync, **“Send Quill to my Kindle”** for one paper, or **“Bring my Kindle notes back”** for notes only. The `$kindle-sync` skill uses the same queue as the five-minute scheduler. On the first sync, papers already in the collection become the baseline. Ask for one by name to send it.

To open the newest annotated PDF when you double-click a paper, install the optional [Kindle Notes plugin for Zotero](zotero-plugin/README.md).
