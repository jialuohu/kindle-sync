FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/kindle-sync ./cmd/kindle-sync

FROM python:3.12-slim-bookworm
ENV VIRTUAL_ENV=/opt/kindle-sync/venv \
    PATH=/opt/kindle-sync/venv/bin:${PATH} \
    PYTHONDONTWRITEBYTECODE=1
COPY helper/requirements.lock /opt/kindle-sync/helper/requirements.lock
RUN python -m venv "$VIRTUAL_ENV" \
    && python -m pip install --disable-pip-version-check --no-cache-dir -r /opt/kindle-sync/helper/requirements.lock \
    && HOME=/tmp/kindle-build-home python -I -c 'import fitz, pyzotero, requests; from zotero_mcp.webdav import upload_attachment_to_webdav' \
    && rm -rf /tmp/kindle-build-home
COPY --from=build /out/kindle-sync /usr/local/bin/kindle-sync
COPY helper/zotero_attachment.py /opt/kindle-sync/helper/
COPY deploy/*.py /opt/kindle-sync/deploy/
ARG APP_UID=1000
ARG APP_GID=1000
RUN install -d -m 0700 -o "$APP_UID" -g "$APP_GID" /var/lib/kindle-sync \
    && test -z "$(ls -A /var/lib/kindle-sync)"
ENV HOME=/var/lib/kindle-sync XDG_CACHE_HOME=/tmp/kindle-sync-cache
USER ${APP_UID}:${APP_GID}
CMD ["python", "-I", "/opt/kindle-sync/deploy/scheduler.py"]
