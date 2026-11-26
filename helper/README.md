# Zotero attachment helper

The Go CLI calls this Python helper to fetch a paper’s stored PDF and archive the annotated copy in the same personal Zotero library. The helper adapts an existing Zotero attachment tool and uses PyZotero, PyMuPDF, and the WebDAV client from `zotero-mcp-server`.

Each export becomes a child attachment named `Kindle Notes - <SHA-256>.pdf`. The hash identifies the contents, so a repeat export of the same bytes reuses the attachment. After writing to Zotero and Koofr, the helper reads the attachment record back and checks the downloaded PDF. The optional desktop plugin recognizes these attachments. [Plugin guide](../zotero-plugin/README.md)

## Development

From the repository root, run the unit tests with `python3 -B -m unittest discover -s helper/tests`.

For the WebDAV integration test, install `helper/requirements.lock` in a Python 3.12 virtual environment. Set `KINDLE_SYNC_TEST_PYTHON` to that environment’s Python executable (absolute path) and run `go test ./internal/zotero`. This test uses a local TLS server for file transfers and an in-memory Zotero client for metadata.
