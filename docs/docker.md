# Deploy with Docker Compose

Run these commands from a source checkout on the Docker host. The `sync` service runs on startup and every five minutes while Docker is running. Manual commands use the same named volume for SQLite and cached PDFs. On a Mac, syncing resumes after the machine wakes and Docker Desktop is running.

## Prepare the image and accounts

Build with the supplied Compose file. It disables the build attestation that can include a private Git remote in image metadata. [Docker build provenance](https://docs.docker.com/reference/compose-file/build/#provenance)

1. Confirm the intended host with `docker context show` and `docker info`. Check `docker compose ls` for an existing deployment. If this library already uses a native installation, follow the migration steps below before starting the scheduler.
2. Put a private copy of `deploy/config.example.json` outside the checkout. Enter the library ID, watched collection, Gmail sender, Kindle address, and Koofr directory. Preserve any artifact-name overrides and configured Drive folder. Use the example's container paths for `state_dir`, `helper_path`, and `helper_python`. See [account setup](setup.md#connect-your-accounts) for values and Amazon's approved-sender setting.
3. Use a dedicated credential directory for this service. It holds `zotero-api-key`, `koofr-username`, `koofr-password`, and `google-client.json`. An existing installation may also have `gmail-token.json` and `drive-token.json`. Keep the directory at mode `0700` and sensitive files at `0600`. The container needs write access to the directory to refresh tokens. If a credential is a symlink to a file outside that directory, copy the file into the dedicated directory with mode `0600` so the container can read it.

Create a private `compose.env` alongside the config. Replace these example paths and IDs. Use `id -u` and `id -g` for the account that owns the files.

```dotenv
KINDLE_SYNC_CONFIG_FILE=/absolute/path/to/kindle-sync/config.json
CODEX_SECRETS_DIR=/absolute/path/to/kindle-sync/secrets
KINDLE_SYNC_UID=1000
KINDLE_SYNC_GID=1000
```

Pin the intended Docker context and let the private file supply the Compose settings. An exported `CODEX_SECRETS_DIR` from another tool could otherwise take precedence.

```sh
compose_env=/absolute/path/to/kindle-sync/compose.env
deploy_context=$(docker context show)
dc() (
  unset KINDLE_SYNC_CONFIG_FILE CODEX_SECRETS_DIR KINDLE_SYNC_UID KINDLE_SYNC_GID KINDLE_SYNC_IMAGE COMPOSE_PROJECT_NAME
  docker --context "$deploy_context" compose --env-file "$compose_env" "$@"
)
dc config --quiet
dc build sync
dc run --rm -T --no-deps sync kindle-sync --help
```

Existing credentials can be reused with the same accounts and Google OAuth client. If a grant is missing or expired, run the affected command and open its consent link in your browser. The callback port is published on the host's loopback interface for this one-off command. For a remote Docker host, also forward local port 43891 over SSH as described in the [account guide](setup.md#connect-your-accounts).

```sh
dc run --rm --no-deps -p 127.0.0.1:43891:43891 -e KINDLE_SYNC_OAUTH_CONTAINER=1 sync kindle-sync auth gmail
dc run --rm --no-deps -p 127.0.0.1:43891:43891 -e KINDLE_SYNC_OAUTH_CONTAINER=1 sync kindle-sync auth drive
```

## Move an existing queue

Stop the old timer and let active commands finish. Keep the source idle throughout the transfer. Save its config and take a private backup of the entire state directory. The transfer includes both the SQLite records and the cached PDFs they refer to.

```sh
umask 077
source_state=/absolute/path/to/old/state
state_backup=/absolute/path/to/new/private-backup
python3 deploy/migrate_state.py backup "$source_state" "$state_backup" &&
dc run --rm -T --no-deps -v "$state_backup:/backup:ro" sync \
  python3 /opt/kindle-sync/deploy/migrate_state.py restore /backup /var/lib/kindle-sync
```

Restore into an empty volume. Compare the original and container `status --json` results by paper key and export ID/version, including delivery state, selected PDF, Gmail ID, Zotero child key and attachment hash when present. Check the baseline and bound folder. Container paths for cached files can differ from the source host. Resume deployment after verifying equivalent records and readable cached files. Keep the source backup private and retire its scheduler.

## Start and verify

For a new installation without a Drive folder, first run the [one-paper test](setup.md#verify-one-paper) using `dc run --rm -T --no-deps sync kindle-sync` in place of `ks`. Use the paper the user selected for the test. When they share it from the Scribe, set `drive_folder_id` and check the returned PDF. An existing installation with a bound folder can proceed directly.

```sh
dc run --rm -T --no-deps sync kindle-sync status --json &&
dc run --rm -T --no-deps sync kindle-sync sync --direction send --dry-run --json &&
dc run --rm -T --no-deps sync kindle-sync sync --direction receive --dry-run --json &&
dc up -d --no-build sync &&
dc ps sync
dc logs --since 15m -f sync
```

Wait for two `sync_finished` events, one on startup and one on the next scheduled round. Record timestamps, exit codes, Gmail submissions, archived notes and problems. An empty queue should yield zero submissions. A preview reads metadata. An actual Gmail submission and a mark visible in a returned PDF verify later stages of the round trip.

For daily operation, use `dc exec -T sync kindle-sync …`. Make it callable from other shells by creating an executable `bin/kindle-sync-agent` in the ignored `bin/` directory of this checkout, or by setting `KINDLE_SYNC_RUNNER` to a private executable. Use the same Docker context, Compose project and environment file as the scheduled service. For example, replace `YOUR_DOCKER_CONTEXT` and the environment file path in this launcher.

```sh
#!/bin/sh
set -eu
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
unset KINDLE_SYNC_CONFIG_FILE CODEX_SECRETS_DIR KINDLE_SYNC_UID KINDLE_SYNC_GID KINDLE_SYNC_IMAGE COMPOSE_PROJECT_NAME
exec docker --context YOUR_DOCKER_CONTEXT compose --env-file /absolute/path/to/kindle-sync/compose.env \
  -f "$repo/compose.yaml" exec -T sync kindle-sync "$@"
```

Check `bin/kindle-sync-agent status --json` against the container's status. When a manual run overlaps a scheduled round, allow that round to finish before trying again.

## Upgrade or roll back

Record the running image before rebuilding. Tag its image ID locally with a unique backup tag. Build the new image while the current service is running, then wait for a completed sync and any manual commands to finish before switching.

```sh
previous_image=$(docker --context "$deploy_context" inspect --format '{{.Image}}' "$(dc ps -q sync)") &&
backup_tag="kindle-sync:backup-$(date -u +%Y%m%dT%H%M%SZ)" &&
docker --context "$deploy_context" image tag "$previous_image" "$backup_tag" &&
dc build sync &&
dc stop sync &&
dc run --rm -T --no-deps sync kindle-sync status --json &&
dc run --rm -T --no-deps sync kindle-sync sync --direction send --dry-run --json &&
dc run --rm -T --no-deps sync kindle-sync sync --direction receive --dry-run --json &&
dc up -d --no-build --force-recreate sync
```

Observe two completed rounds again. If the upgrade fails after the old service stops, set `KINDLE_SYNC_IMAGE` to the value of `$backup_tag` in the private `compose.env`, then recreate the service with the same project and volume.

```sh
dc up -d --no-build --force-recreate sync
```

Retain the rollback tag in the private Compose environment until you next upgrade. Keep the current data volume when reverting the application. A database backup made before a Gmail submission lacks its delivery record and can cause a duplicate send.
