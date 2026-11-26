#!/bin/sh
# Run on the Linux server from an extracted release with Python 3.12 + venv.
set -eu
base=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
prefix=/opt/kindle-sync
test "$(id -u)" = 0 || { echo "Run the release installer as root." >&2; exit 1; }
test -x "$base/bin/kindle-sync"
command -v python3.12 >/dev/null
if ! id kindle-sync >/dev/null 2>&1; then
    useradd --system --user-group --home-dir /var/lib/kindle-sync --shell /usr/sbin/nologin kindle-sync
fi
install -d -m 0755 "$prefix/bin" "$prefix/helper" /etc/kindle-sync
install -d -o kindle-sync -g kindle-sync -m 0700 /var/lib/kindle-sync /etc/kindle-sync/secrets
install -m 0644 "$base/helper/zotero_attachment.py" "$base/helper/requirements.lock" "$prefix/helper/"
python3.12 -m venv "$prefix/venv"
"$prefix/venv/bin/python" -m pip install --disable-pip-version-check -r "$prefix/helper/requirements.lock"
"$prefix/venv/bin/python" -I -c 'import fitz, pyzotero, requests; from zotero_mcp.webdav import upload_attachment_to_webdav'
install -m 0755 "$base/bin/kindle-sync" "$prefix/bin/"
if [ ! -e /etc/kindle-sync/config.json ]; then
    install -m 0640 -o root -g kindle-sync "$base/deploy/config.example.json" /etc/kindle-sync/config.json
fi
install -m 0644 "$base/deploy/kindle-sync.service" "$base/deploy/kindle-sync.timer" /etc/systemd/system/
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
fi
