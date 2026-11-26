package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kindle-sync/internal/model"
)

// Exercise the same command entry point as systemd, including real SQLite,
// credential loading, Google serialization, and the helper subprocess contract.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCommandsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("requires python3 for external helper fixture")
	}
	configPath := filepath.Join(dir, "config.json")
	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(name, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	c := config{LibraryID: "123", Collection: "READ0001", Sender: "sender@example.invalid", Kindle: "reader@example.invalid", StateDir: filepath.Join(dir, "state"), WebDAV: "https://storage.example.invalid/zotero/", HelperPath: filepath.Join(dir, "fixture.py"), HelperPython: python}
	write(configPath, encode(c))
	write(c.HelperPath, []byte(`
import sys, pathlib, json, hashlib
def opt(k): return sys.argv[sys.argv.index(k)+1]
if sys.argv[1] == 'fetch':
 data = b'%PDF-1.4\nfixture\n%%EOF'
 pathlib.Path(opt('--output')).write_bytes(data)
 print(json.dumps({'status':'downloaded','attachment_key':opt('--attachment-key'),'md5':hashlib.md5(data).hexdigest()}))
elif sys.argv[1] == 'attach':
 f = pathlib.Path(opt('--file'))
 assert f.read_bytes().endswith(b'handwritten')
 print(json.dumps({'status':'created','backend':'webdav','attachment_key':'NOTES001','parent_key':opt('--parent-key'),'basename':f.name,'md5':hashlib.md5(f.read_bytes()).hexdigest()}))
`))
	t.Setenv("KINDLE_SYNC_CONFIG", configPath)
	t.Setenv("CODEX_SECRETS_DIR", dir)
	for _, n := range []string{"zotero-api-key", "koofr-username", "koofr-password"} {
		write(filepath.Join(dir, n), []byte("fixture"))
	}
	write(filepath.Join(dir, "google-client.json"), []byte(`{"installed":{"client_id":"fixture","client_secret":"fixture","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","redirect_uris":["http://localhost"]}}`))
	for _, svc := range []string{"gmail", "drive"} {
		write(filepath.Join(dir, svc+"-token.json"), encode(map[string]any{"service": svc, "client_id": "fixture", "email": c.Sender, "token": map[string]any{"access_token": "fixture", "refresh_token": "fixture", "expiry": time.Now().Add(time.Hour)}}))
	}
	var havePaper bool
	var sends int
	var exportName string
	returned := []byte("%PDF-1.4\nhandwritten")
	export := func() map[string]any {
		return map[string]any{"id": "export", "name": exportName, "parents": []string{"folder"}, "mimeType": "application/pdf", "size": strconv.Itoa(len(returned)), "md5Checksum": fmt.Sprintf("%x", md5.Sum(returned)), "version": "1"}
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		var body []byte
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/READ0001/items/top"):
			body = []byte("[]")
			if havePaper {
				body = []byte(`[{"key":"PAPER001","data":{"itemType":"journalArticle","title":"Shared title"}}]`)
			}
		case strings.HasSuffix(path, "/READ0001/collections"):
			body = []byte("[]")
		case strings.HasSuffix(path, "/PAPER001/children"):
			body = []byte(`[{"key":"FILE0001","data":{"itemType":"attachment","parentItem":"PAPER001","linkMode":"imported_url","contentType":"application/pdf","filename":"original.pdf"}}]`)
		case strings.HasSuffix(path, "/messages/send"):
			sends++
			body = encode(map[string]string{"id": fmt.Sprintf("gmail-%d", sends)})
		case strings.HasSuffix(path, "/files/folder"):
			body = []byte(`{"mimeType":"application/vnd.google-apps.folder","trashed":false}`)
		case strings.HasSuffix(path, "/files"):
			body = encode(map[string]any{"files": []any{export()}})
		case strings.HasSuffix(path, "/files/export") && r.URL.Query().Get("alt") == "media":
			body = returned
		case strings.HasSuffix(path, "/files/export"):
			body = encode(export())
		default:
			return nil, fmt.Errorf("unexpected test request %s", path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})
	invoke := func(args ...string) []byte {
		t.Helper()
		var b bytes.Buffer
		if err := run(context.Background(), args, &b); err != nil {
			t.Fatalf("%v: %v; %s", args, err, b.Bytes())
		}
		return b.Bytes()
	}
	invoke("sync", "--dry-run", "--json")
	if _, err := os.Stat(c.StateDir); !os.IsNotExist(err) {
		t.Fatal("preview wrote local state")
	}
	invoke("sync")
	havePaper = true
	invoke("sync")
	invoke("sync")
	status := func() ([]model.Job, []model.Receipt) {
		t.Helper()
		var v struct {
			Jobs     []model.Job
			Receipts []model.Receipt
		}
		if err := json.Unmarshal(invoke("status", "--json"), &v); err != nil {
			t.Fatal(err)
		}
		return v.Jobs, v.Receipts
	}
	jobs, _ := status()
	if len(jobs) != 1 || jobs[0].State != "submitted" || sends != 1 {
		t.Fatal(jobs, sends)
	}
	token := jobs[0].Token
	rejectInvalid := func(decision ...string) {
		t.Helper()
		if err := run(context.Background(), append([]string{"resolve", "PAPER001"}, decision...), io.Discard); err == nil {
			t.Fatalf("accepted invalid resolution %q", decision)
		}
	}
	rejectInvalid("retry")
	rejectInvalid("rejected")
	rejectInvalid("rejected", "wrong-id")
	invoke("resolve", "PAPER001", "rejected", "gmail-1")
	rejectInvalid("rejected", "gmail-1")
	jobs, _ = status()
	if sends != 1 || jobs[0].State != "queued" || jobs[0].GmailID != "" || len(jobs[0].RejectedGmailIDs) != 1 || jobs[0].RejectedGmailIDs[0] != "gmail-1" {
		t.Fatalf("rejection was not recorded: %+v, sends=%d", jobs, sends)
	}
	invoke("sync")
	rejectInvalid("rejected", "gmail-1")
	jobs, _ = status()
	if sends != 2 || jobs[0].State != "submitted" || jobs[0].GmailID != "gmail-2" || jobs[0].Token != token || len(jobs[0].RejectedGmailIDs) != 1 || jobs[0].RejectedGmailIDs[0] != "gmail-1" {
		t.Fatalf("retry did not preserve delivery history: %+v, sends=%d", jobs, sends)
	}
	// A mistyped first folder must not strand the original delivery token.
	c.Folder = "missing"
	write(configPath, encode(c))
	var failed bytes.Buffer
	if err := run(context.Background(), []string{"sync"}, &failed); err == nil {
		t.Fatal("accepted missing folder")
	}
	c.Folder = "folder"
	write(configPath, encode(c))
	exportName = jobs[0].Token + " Shared title.pdf"
	invoke("sync")
	invoke("sync")
	_, receipts := status()
	if sends != 2 || len(receipts) != 1 || receipts[0].State != "archived" || receipts[0].Child != "NOTES001" || receipts[0].Parent != "PAPER001" {
		t.Fatal(receipts, sends)
	}
}
