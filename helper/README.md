# Zotero attachment helper

Kindle Sync calls this Python helper for two operations on a personal Zotero library.

- **Send a paper.** Download the source PDF from Koofr WebDAV and verify its checksum before the CLI emails it.
- **Save notes.** Store the annotated PDF under its original Zotero item as `Kindle Notes - <SHA-256>.pdf`. The hash identifies the file contents. The helper reads back the Zotero record and downloads the stored PDF to verify it.

Once the new copy passes verification, the CLI moves older Kindle Notes attachments to Zotero Trash. To open the current copy by double-clicking the paper, install the [Kindle Notes plugin](../zotero-plugin/README.md).

## Run the tests

Run the Python unit tests from the repository root.

```sh
python3 -B -m unittest discover -s helper/tests
```

The WebDAV integration tests use a local HTTPS server and an in-memory Zotero client. They exercise PDF downloads and uploads with PyMuPDF, PyZotero, and the WebDAV client from `zotero-mcp-server`. Install the dependencies in a Python 3.12 environment and point Go to its interpreter.

```sh
python3.12 -m venv .venv
.venv/bin/python -m pip install -r helper/requirements.lock
KINDLE_SYNC_TEST_PYTHON="$PWD/.venv/bin/python" go test -count=1 -run 'Test(Fetch|Archive)ViaWebDAV' ./internal/zotero
```
