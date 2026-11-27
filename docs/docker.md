# Deploy with Docker Compose

Run these commands from the repository root on the Docker host. The `sync` service checks for papers and notes at startup and every five minutes. On a Mac, the host must be awake and Docker Desktop running. The scheduler and CLI share a Docker volume containing the delivery records and cached PDFs.

For an existing installation, identify its Docker context, project, and state volume. To replace a running Compose image, define the shell shortcuts below, then go to [Upgrade or roll back](#upgrade-or-roll-back). To bring an existing database into Compose, see [Move an existing queue](#move-an-existing-queue).

## Prepare the image and accounts

1. Run `docker context show`, `docker info`, and `docker compose ls` to check the host and running projects.
2. Copy [the example config](../deploy/config.example.json) to a private directory outside the checkout. Fill in the account settings and approve the sender on Amazon. Keep the example's container paths for `state_dir`, `helper_path`, and `helper_python`. For a migration, copy the existing folder ID and artifact names. [Account settings and credentials](setup.md#connect-your-accounts)
3. Create a dedicated secrets directory for the service. Store the Zotero API key, Koofr credentials, and Google client file there. Set the directory to mode `0700` and the files to `0600`. Copy in any credentials that are symlinks to files outside the directory. The container needs write access to save and refresh Google tokens.

Create `compose.env` beside the private config. Set the two paths to their absolute locations on the host. Use `id -u` and `id -g` for the account that owns the files.

```dotenv
KINDLE_SYNC_CONFIG_FILE=/absolute/path/to/kindle-sync/config.json
CODEX_SECRETS_DIR=/absolute/path/to/kindle-sync/secrets
KINDLE_SYNC_UID=1000
KINDLE_SYNC_GID=1000
```

Define these shortcuts in the same shell. Replace the environment file path and, for an existing deployment, use its project name. `dc` pins the project, Docker context, and Compose file. `ks` runs a one-off CLI command with that project's credentials and state volume.

```sh
compose_env=/absolute/path/to/kindle-sync/compose.env
deploy_context=$(docker context show)
deploy_project=kindle-sync
repo=$(pwd -P)
dc() (
  unset KINDLE_SYNC_CONFIG_FILE CODEX_SECRETS_DIR KINDLE_SYNC_UID KINDLE_SYNC_GID KINDLE_SYNC_IMAGE
  docker --context "$deploy_context" compose --project-name "$deploy_project" \
    --env-file "$compose_env" -f "$repo/compose.yaml" "$@"
)
ks() {
  dc run --rm -T --no-deps sync kindle-sync "$@"
}
```

Build the image and check the CLI. The supplied Compose file disables build provenance to keep the private Git remote out of the image metadata. [Docker build provenance](https://docs.docker.com/reference/compose-file/build/#provenance)

```sh
dc config --quiet && dc build sync && ks --help
```

### Authorize Gmail and Google Drive

Docker uses its own [Google desktop OAuth client](setup.md#save-the-credentials) so it can sync when your agent is offline. For Gmail, sign in with the Amazon-approved sender and allow sending and email address verification. For Drive, grant read-only access to the account linked to your Scribe. The accounts can differ.

Reuse valid tokens for the same accounts and OAuth client. Run only the command for a service that needs authorization, then open its consent link in your browser. For a remote Docker host, first [forward port 43891 over SSH](setup.md#connect-your-accounts).

```sh
dc run --rm --no-deps -p 127.0.0.1:43891:43891 -e KINDLE_SYNC_OAUTH_CONTAINER=1 sync kindle-sync auth gmail
dc run --rm --no-deps -p 127.0.0.1:43891:43891 -e KINDLE_SYNC_OAUTH_CONTAINER=1 sync kindle-sync auth drive
```

## Start and verify

For a new installation, choose one paper and [verify its round trip](setup.md#verify-one-paper) with the Compose `ks` shortcut above. This identifies the Scribe's Drive folder and verifies that a handwritten mark reaches Zotero. For an existing installation, first [move the queue](#move-an-existing-queue) if it lives outside the target volume.

Preview both directions. If both succeed, start the scheduler and follow its log.

```sh
ks status --json &&
ks sync --direction send --dry-run --json &&
ks sync --direction receive --dry-run --json &&
dc up -d --no-build sync &&
dc ps sync &&
dc logs --since 15m -f sync
```

Look for a `sync_finished` event after startup and another about five minutes later. Each event shows the time, exit code, Gmail submissions (`submitted`), notes saved (`archived`), and `problems`. An empty queue should show zero submissions and zero errors. Press Ctrl+C to leave the log. The service keeps running.

## Run the CLI from an agent

Use `dc exec -T sync kindle-sync status --json` to inspect the running service. For the [sync skill](../.agents/skills/kindle-sync/SKILL.md), save the following launcher as `bin/kindle-sync-agent` under the checkout. Create `bin/` if needed. Replace the context, project name, and environment file path with the values used above.

```sh
#!/bin/sh
set -eu
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
unset KINDLE_SYNC_CONFIG_FILE CODEX_SECRETS_DIR KINDLE_SYNC_UID KINDLE_SYNC_GID KINDLE_SYNC_IMAGE
exec docker --context YOUR_DOCKER_CONTEXT compose --project-name YOUR_COMPOSE_PROJECT \
  --env-file /absolute/path/to/kindle-sync/compose.env -f "$repo/compose.yaml" \
  exec -T sync kindle-sync "$@"
```

Run `chmod 700 bin/kindle-sync-agent`, then `bin/kindle-sync-agent status --json`. The result should match the container's status. To use a launcher elsewhere, point `KINDLE_SYNC_RUNNER` to a private executable that calls the same service. If the scheduler already holds the queue lock, retry after its round finishes.

## Move an existing queue

Stop the old scheduler and wait for active sync commands to finish. Save the old config and `status --json` output privately. Back up the SQLite database and cached PDFs together. Choose a new backup path in an existing private directory and an empty destination volume.

```sh
umask 077
source_state=/absolute/path/to/old/state
state_backup=/absolute/path/to/new/private-backup
python3 deploy/migrate_state.py backup "$source_state" "$state_backup" &&
dc run --rm -T --no-deps -v "$state_backup:/backup:ro" sync \
  python3 /opt/kindle-sync/deploy/migrate_state.py restore /backup /var/lib/kindle-sync
```

Compare `ks status --json` with the saved status. Check the initial collection scan, jobs by paper key, and receipts by Drive file ID and version. Compare delivery states, selected PDFs, Gmail IDs, Zotero attachment keys, and hashes. Verify the configured Drive folder and that the container can read the restored PDFs. Their paths can differ from the old host.

Keep the old scheduler stopped. Continue with [Start and verify](#start-and-verify).

## Upgrade or roll back

Use the same context, Compose project, and state volume. Tag the running image before building its replacement. If `compose.env` still selects a rollback tag, clear that override before building so the backup keeps its tag.

```sh
previous_image=$(docker --context "$deploy_context" inspect --format '{{.Image}}' "$(dc ps -q sync)") &&
backup_tag="kindle-sync:backup-$(date -u +%Y%m%dT%H%M%SZ)" &&
docker --context "$deploy_context" image tag "$previous_image" "$backup_tag" &&
dc build sync
```

Save the value of `$backup_tag` on the host. After the build succeeds, wait for active sync commands to finish. Preview the new image against the existing queue before restarting.

```sh
dc stop sync &&
ks status --json &&
ks sync --direction send --dry-run --json &&
ks sync --direction receive --dry-run --json &&
dc up -d --no-build --force-recreate sync
```

[Check the next two rounds](#start-and-verify). If the upgrade fails after the old service stops, set `KINDLE_SYNC_IMAGE` in the private `compose.env` to the saved tag. Keep the current data volume. Restoring an older database can resend papers delivered since the backup.

Preview returns before starting the rollback image. Older releases can re-import notes that the current release has retired. Review the proposed actions, then start the service if they match your expectations.

```sh
ks sync --direction receive --dry-run --json
```

```sh
dc up -d --no-build --force-recreate sync
```
