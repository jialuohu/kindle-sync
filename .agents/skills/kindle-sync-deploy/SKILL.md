---
name: kindle-sync-deploy
description: Deploy, migrate, verify, or upgrade the Kindle Sync service with Docker Compose. Use for operating the scheduled service that connects Zotero, Kindle Scribe, and Google Drive.
---

# Deploy Kindle Sync

Follow the [Compose deployment guide](../../../docs/docker.md) from the repository root. Identify the intended Docker context, Compose project, current scheduler, and state volume before writing. Keep account settings on the deployment host and credential files in its private secrets directory.

1. **Inspect.** Check Docker, Compose, the current image and database. An existing native installation or a replacement Compose project may already own the same reading queue.
2. **Prepare.** Build the image, configure the mounts, and verify the CLI and helper. On a migration, let the old writer finish, take a private backup, and transfer the complete SQLite state and cached PDFs. Check each job and receipt, the initial collection baseline, and the bound Drive folder before enabling the scheduler. Record the previous image on upgrades.
3. **Activate.** Point the daily [Kindle Sync skill](../kindle-sync/SKILL.md) at this same instance. Start the five-minute scheduler once the configured library and Drive folder pass preflight. Run interactive Google consent only if the saved authorization is absent or unusable. Let the user complete any browser or Kindle interaction.
4. **Verify.** Observe the first completed round and the next scheduled one. Report their times, Gmail submissions, saved notes and errors, including zero work. For preview-only requests, stop after preflight. A failed first round warrants inspecting its report and state before retrying.

If an upgrade fails, use the previous application image with the current data volume. Restoring an old database after sending can deliver papers twice. Keep backups, tokens, private paths and user data out of the checkout and logs.
