# Zotero attachment helper

The Go CLI calls this Python helper to download stored PDFs from a personal Zotero library and upload annotated PDFs to Koofr. It uses PyZotero, PyMuPDF, and the WebDAV client from `zotero-mcp-server`. Pass the HTTPS storage directory containing Zotero's ZIP and property files.

## Development

Install `helper/requirements.lock` in a Python 3.12 virtual environment. From the repository root, run `python3 -B -m unittest discover -s helper/tests`. Set `KINDLE_SYNC_TEST_PYTHON` to the virtual environment's Python executable (absolute path) and run `go test ./internal/zotero` for the local WebDAV integration test. It checks the archive member name, the property file checksum and the downloaded PDF against a local TLS server with an in-memory Zotero metadata client.
