---
name: kindle-sync-deploy
description: Install, migrate, verify, or upgrade the scheduled Kindle Sync service with Docker Compose.
---

# Deploy Kindle Sync

Run commands from the checkout on the Docker host. Follow the [Docker Compose guide](../../../docs/docker.md) for the commands and private configuration.

1. Identify the Docker context, Compose project, scheduler, and state volume. Check for a native installation or another Compose project using the same queue. For an existing installation, see [Migrate or upgrade](#migrate-or-upgrade) before rebuilding or moving its data.
2. Build the image, configure its mounts, and check the CLI and PDF helper. Keep account settings, backups, and credentials outside the checkout. Store credentials in the deployment's private secrets directory and keep private paths, tokens, and user data out of logs.
3. Reuse valid Google authorization. Let the user complete any browser sign-in or Kindle export. Preview the send from the configured Zotero collection. Once the Drive folder is set, preview the return. For a preview-only request, report any missing setup and finish here. For a new deployment without a folder, follow the guide's one-paper test with the user's chosen paper.
4. Start the five-minute scheduler. Point the [daily sync skill](../kindle-sync/SKILL.md) at the same deployment and verify that its runner sees the same queue.
5. Observe the startup sync and the next scheduled sync. Report each round's time, Gmail submissions, archived notes, and errors, including zero counts. If a round fails, inspect the report and the saved state before retrying.

## Migrate or upgrade

Before transferring a queue, stop its scheduler and let the active sync finish. Make a private backup of the entire state directory. Transfer the SQLite database and cached PDFs together. Before starting the new scheduler, compare the jobs, receipts, initial collection baseline, and bound Drive folder with the source.

For an upgrade, tag the running image before rebuilding. If the upgrade fails, run the previous image with the current state volume. Restoring a database from before a Gmail submission can send the paper twice.
