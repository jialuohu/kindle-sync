# Kindle Sync

Read papers from Zotero on a Kindle Scribe and save your handwritten notes back to the same Zotero item.

## Use with an agent

Any agent that can read this checkout and run commands on the deployment host can follow these skills. Ask for the skill by name if your agent discovers it automatically, or copy one of these prompts.

- **[Deploy or upgrade](.agents/skills/kindle-sync-deploy/SKILL.md)** — “Read `.agents/skills/kindle-sync-deploy/SKILL.md` and deploy Kindle Sync on this Mac.”
- **[Sync papers and notes](.agents/skills/kindle-sync/SKILL.md)** — “Read `.agents/skills/kindle-sync/SKILL.md` and preview what would sync.” You can also ask it to send unsent papers in ReadLater or bring back your Kindle notes.

The first scan records papers already in the collection. Ask the agent to send those papers when you are ready. Papers added later go out automatically every five minutes while the service is running.

## Everyday use

1. Add a paper to the Zotero collection you watch, or to any of its subcollections. Kindle Sync fetches the PDF from Koofr WebDAV and emails it to your Kindle. Each paper gets its own message.
2. Write on the PDF. When you're ready to send it back, choose **Share → Google Drive → Upload** on the Scribe.
3. After the next sync, open **Kindle Notes** under the paper in Zotero. Each paper keeps its latest annotated PDF. When a newer export changes the PDF, Kindle Sync verifies the new copy, then moves its previous copy to Zotero Trash. The original paper and your Drive exports stay in place.

You can move a paper out of your reading collection after sending it. Notes still return to the same Zotero item. If you trash or delete the item, or an export doesn't match a paper Kindle Sync sent, the PDF stays in Drive.

Kindle filenames use the first author's surname, a two-digit year, and the paper's artifact name. A 2026 paper by Reed introducing *Quill* becomes `reed26-quill.pdf`. If publication metadata and the PDF give no year, the filename uses the year the item was added to Zotero. [Filename rules](docs/setup.md#kindle-filenames)

## Setup guides

- [Docker Compose](docs/docker.md) for installation, migration, and upgrades.
- [Native Linux service](docs/setup.md) for a systemd installation.
- [Kindle Notes plugin for Zotero](zotero-plugin/README.md) to open the latest annotated PDF when you double-click a paper.
