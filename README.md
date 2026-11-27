# Kindle Sync

Read Zotero papers on your Kindle Scribe. Export your annotations to Google Drive and Kindle Sync saves the marked-up PDF alongside the original.

<picture>
  <source media="(max-width: 600px)" srcset="docs/assets/kindle-workflow-mobile.webp">
  <img src="docs/assets/kindle-workflow.webp" alt="From Zotero, Kindle Sync sends the PDF from Koofr through Gmail and Amazon Send to Kindle to your Kindle Scribe. After annotating, tap Share → Google Drive → Upload. Kindle Sync attaches the marked-up PDF to the original Zotero item.">
</picture>

## Use with an agent

**Docker Compose is the recommended setup.** Paste this prompt into an agent with terminal access to the deployment host.

```text
Clone https://github.com/jialuohu/kindle-sync.git
Use .agents/skills/kindle-sync-deploy/SKILL.md to deploy here with Docker Compose.
Guide me through sign-in, local credential setup, and any steps on my Kindle.
Check the startup sync and the next scheduled sync. Report papers sent,
notes saved, and any errors.
```

After setup, tell your agent **“Sync my Kindle.”** It sends new papers and brings your exported notes back to Zotero. [Deployment skill](.agents/skills/kindle-sync-deploy/SKILL.md) · [Sync skill](.agents/skills/kindle-sync/SKILL.md)

## Everyday use

1. **In Zotero**, add a paper to your reading collection or a subcollection. Every five minutes, Kindle Sync checks for new papers and emails their PDFs from Koofr WebDAV via Gmail.
2. **On your Scribe**, annotate the paper, then tap **Share → Google Drive → Upload**.
3. **Back in Zotero**, open **Kindle Notes** under the original paper after the next sync. It holds your latest export.

To include papers already in the collection at setup, ask your agent to “Send the unsent papers in ReadLater.”

## Guides

- [Deploy with Docker Compose](docs/docker.md) or [install on Linux with systemd](docs/setup.md#install-on-linux)
- [Connect your accounts](docs/setup.md#connect-your-accounts) and [verify one paper](docs/setup.md#verify-one-paper)
- [Kindle filenames](docs/setup.md#kindle-filenames) and [which exports return to Zotero](docs/setup.md#which-exports-return-to-zotero)
- [Open Kindle Notes by default in Zotero](zotero-plugin/README.md)
