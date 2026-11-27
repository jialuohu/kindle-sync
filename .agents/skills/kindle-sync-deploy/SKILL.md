---
name: kindle-sync-deploy
description: Install, migrate, verify, or upgrade the scheduled Kindle Sync service with Docker Compose.
---

# Deploy Kindle Sync

Run commands from the checkout on the Docker host. Follow the [Docker Compose guide](../../../docs/docker.md) for the commands and private configuration.

1. Identify the Docker context, Compose project, scheduler, and state volume. Check for a native installation or another Compose project using the same queue. For an existing installation, see [Migrate or upgrade](#migrate-or-upgrade) before rebuilding or moving its data.
2. Build the image, configure its mounts, and check the CLI and PDF helper. Keep account settings, backups, and credentials outside the checkout. Store credentials in the deployment's private secrets directory and keep private paths, tokens, and user data out of logs.
3. Docker needs its own Google authorization to sync while the agent is offline. Reuse valid grants. If a grant is missing or revoked, guide the user through sign-in for that service. Gmail uses the Amazon-approved sender. Drive uses the account linked to the Scribe. These accounts can differ. [Google sign-in for Compose](../../../docs/docker.md#authorize-gmail-and-google-drive)
4. Preview the configured directions. For a preview-only request, report the result and any missing setup, then stop. On a new installation, [test one paper](../../../docs/setup.md#verify-one-paper). Once the reader exports it, use their folder link or an official Drive connector already signed in to the Scribe's account to identify the export's parent folder. If the folder is ambiguous, ask the reader. Save its ID in `drive_folder_id` and verify the return.
5. Start the five-minute scheduler after the guide's checks pass. Point the [daily sync skill](../kindle-sync/SKILL.md) at the same deployment and verify that its runner sees the same queue.
6. Observe the startup sync and the next scheduled sync. Report each round's time, Gmail submissions, archived notes, and errors, including zero counts. If a round fails, inspect the report and the saved state before retrying.

## Migrate or upgrade

Before transferring a queue, stop its scheduler and let the active sync finish. Make a private backup of the entire state directory. Transfer the SQLite database and cached PDFs together. Before starting the new scheduler, compare the jobs, receipts, initial collection baseline, and bound Drive folder with the source.

For an upgrade, tag the running image before rebuilding. If the upgrade fails, run the previous image with the current state volume. Restoring a database from before a Gmail submission can send the paper twice.
