package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
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
 data = f.read_bytes()
 assert data.endswith(b'handwritten')
 child = 'NOTES002' if b'revised' in data else 'NOTES001'
 print(json.dumps({'status':'created','backend':'webdav','attachment_key':child,'parent_key':opt('--parent-key'),'basename':f.name,'md5':hashlib.md5(data).hexdigest()}))
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
	originalNotes := []byte("%PDF-1.4\nhandwritten")
	revisedNotes := []byte("%PDF-1.4\nrevised handwritten")
	returned := originalNotes
	exportVersion := 1
	exportDate := time.Now().Add(time.Minute).UTC()
	export := func() map[string]any {
		return map[string]any{
			"id": "export", "name": exportName, "parents": []string{"folder"},
			"createdTime":  exportDate.Format(time.RFC3339Nano),
			"modifiedTime": exportDate.Add(time.Duration(exportVersion) * time.Minute).Format(time.RFC3339Nano),
			"mimeType":     "application/pdf", "size": strconv.Itoa(len(returned)),
			"md5Checksum": fmt.Sprintf("%x", md5.Sum(returned)), "version": strconv.Itoa(exportVersion),
		}
	}
	notesRecord := func(key string, pdf []byte, deleted bool) map[string]any {
		data := map[string]any{
			"key": key, "itemType": "attachment", "linkMode": "imported_file",
			"contentType": "application/pdf", "parentItem": "PAPER001",
			"filename": fmt.Sprintf("Kindle Notes - %x.pdf", sha256.Sum256(pdf)), "md5": fmt.Sprintf("%x", md5.Sum(pdf)),
		}
		if deleted {
			data["deleted"] = 1
		}
		return map[string]any{"key": key, "data": data}
	}
	var retireWrites int
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		var body []byte
		header := http.Header{"Content-Type": {"application/json"}}
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/READ0001/items/top"):
			body = []byte("[]")
			if havePaper {
				body = []byte(`[{"key":"PAPER001","data":{"itemType":"journalArticle","title":"Indexing library records","creators":[{"creatorType":"author","lastName":"Reed","firstName":"A."}],"date":"2026"}}]`)
			}
		case strings.HasSuffix(path, "/READ0001/collections"):
			body = []byte("[]")
		case path == "/users/123/items/PAPER001":
			body = []byte(`{"key":"PAPER001","data":{"key":"PAPER001","itemType":"journalArticle"}}`)
		case path == "/users/123/items" && r.Method == http.MethodGet:
			header.Set("Last-Modified-Version", strconv.Itoa(42+retireWrites))
			body = encode([]any{
				map[string]any{"key": "PAPER001", "data": map[string]string{"key": "PAPER001", "itemType": "journalArticle"}},
				notesRecord("NOTES001", originalNotes, retireWrites > 0),
				notesRecord("NOTES002", revisedNotes, false),
			})
		case path == "/users/123/items" && r.Method == http.MethodPost:
			var changes []struct {
				Key     string
				Deleted int
			}
			if err := json.NewDecoder(r.Body).Decode(&changes); err != nil || len(changes) != 1 || changes[0].Key != "NOTES001" || changes[0].Deleted != 1 || r.Header.Get("If-Unmodified-Since-Version") != "42" {
				return nil, fmt.Errorf("unexpected attachment update: %+v", changes)
			}
			retireWrites++
			body = []byte(`{"successful":{"0":{"key":"NOTES001"}},"failed":{}}`)
		case strings.HasSuffix(path, "/PAPER001/children"):
			body = []byte(`[{"key":"FILE0001","data":{"itemType":"attachment","parentItem":"PAPER001","linkMode":"imported_url","contentType":"application/pdf","filename":"original.pdf"}}]`)
		case strings.HasSuffix(path, "/messages/send"):
			sent, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Contains(sent, []byte("filename=reed26-quill.pdf")) {
				t.Error("outgoing Gmail MIME did not name the attachment reed26-quill.pdf")
			}
			sends++
			body = encode(map[string]string{"id": fmt.Sprintf("gmail-%d", sends)})
		case strings.HasSuffix(path, "/files/folder"):
			body = []byte(`{"mimeType":"application/vnd.google-apps.folder","trashed":false}`)
		case strings.HasSuffix(path, "/files"):
			other := map[string]string{"id": "unrelated", "name": "unrelated.pdf", "version": "1"}
			body = encode(map[string]any{"files": []any{other, export()}})
		case strings.HasSuffix(path, "/files/export") && r.URL.Query().Get("alt") == "media":
			body = returned
		case strings.HasSuffix(path, "/files/export"):
			body = encode(export())
		default:
			return nil, fmt.Errorf("unexpected test request %s", path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
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
	if got := string(invoke("sync", "--dry-run", "--item", "PAPER001", "--artifact", "Quill")); !strings.Contains(got, "reed26-quill.pdf") {
		t.Fatal("preview omitted the requested filename:", got)
	}
	invoke("sync", "--item", "PAPER001", "--artifact", "Quill")
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
	if jobs[0].OutboundName != "reed26-quill.pdf" {
		t.Fatalf("unexpected recorded filename: %q", jobs[0].OutboundName)
	}
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
	if sends != 2 || jobs[0].State != "submitted" || jobs[0].GmailID != "gmail-2" || jobs[0].OutboundName != "reed26-quill.pdf" || len(jobs[0].RejectedGmailIDs) != 1 || jobs[0].RejectedGmailIDs[0] != "gmail-1" {
		t.Fatalf("retry did not preserve delivery history: %+v, sends=%d", jobs, sends)
	}
	// A mistyped first folder must not strand the original delivery.
	c.Folder = "missing"
	write(configPath, encode(c))
	var failed bytes.Buffer
	if err := run(context.Background(), []string{"sync"}, &failed); err == nil {
		t.Fatal("accepted missing folder")
	}
	c.Folder = "folder"
	write(configPath, encode(c))
	exportName = "reed26-quill - Kindle.pdf"
	invoke("receive", "export", "--json")
	invoke("sync", "--direction", "receive")
	if got := string(invoke("receive", "unrelated")); !strings.Contains(got, "no matching delivery") {
		t.Fatal("targeted receive did not explain the skipped export:", got)
	}
	_, receipts := status()
	if sends != 2 || len(receipts) != 1 || receipts[0].State != "archived" || receipts[0].Child != "NOTES001" || receipts[0].Parent != "PAPER001" {
		t.Fatal(receipts, sends)
	}
	exportVersion, returned = 2, revisedNotes
	invoke("sync", "--direction", "receive", "--dry-run")
	if retireWrites != 0 {
		t.Fatal("preview retired an attachment")
	}
	invoke("sync", "--direction", "receive")
	invoke("sync", "--direction", "receive")
	_, receipts = status()
	var active []string
	for _, receipt := range receipts {
		if receipt.State == "archived" {
			active = append(active, receipt.Child)
		}
	}
	if len(active) != 1 || active[0] != "NOTES002" || retireWrites != 1 || sends != 2 {
		t.Fatalf("active notes %v, trash writes %d, sends %d", active, retireWrites, sends)
	}
}

func TestFindUsesOnlyLiveZoteroMetadata(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	stateDir := filepath.Join(dir, "state")
	b, err := json.Marshal(config{LibraryID: "123", Collection: "READ0001", StateDir: stateDir, ArtifactNames: map[string]string{"PAPER001": "Quill"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zotero-api-key"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KINDLE_SYNC_CONFIG", configPath)
	t.Setenv("CODEX_SECRETS_DIR", dir)
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Host != "api.zotero.org" || r.Header.Get("Zotero-API-Key") != "fixture" {
			return nil, fmt.Errorf("unexpected lookup request %s %s", r.Method, r.URL)
		}
		var data string
		switch r.URL.Path {
		case "/keys/current":
			data = `{"userID":123,"username":"example-user"}`
		case "/users/123/collections/READ0001/items/top":
			data = `[{"key":"PAPER001","data":{"itemType":"journalArticle","title":"Catalog Records","date":"2026","creators":[{"creatorType":"author","lastName":"Reed"}]}}]`
		case "/users/123/collections/READ0001/collections":
			data = `[{"key":"NEST0001"}]`
		case "/users/123/collections/NEST0001/items/top":
			data = `[{"key":"PAPER002","data":{"itemType":"journalArticle","title":"Catalog Records about PAPER009","shortTitle":"New Index","abstractNote":"We present Leaf, a catalog index."}}]`
		case "/users/123/collections/NEST0001/collections":
			data = "[]"
		default:
			return nil, fmt.Errorf("unexpected metadata path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
	})
	for _, tc := range []struct {
		query, keys string
	}{
		{"catalog    records", "PAPER001 PAPER002"},
		{"new INDEX", "PAPER002"},
		{"Leaf", "PAPER002"},
		{"QUILL", "PAPER001"},
		{"PAPER002", "PAPER002"},
		{"PAPER009", ""},
		{"zotero://select/library/items/PAPER001", "PAPER001"},
		{"https://www.zotero.org/example-user/collections/NEST0001/items/PAPER002/collection", "PAPER002"},
		{"https://api.zotero.org/users/123/items/PAPER001", "PAPER001"},
		{"missing", ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(context.Background(), []string{"find", tc.query, "--json"}, &out); err != nil {
				t.Fatal(err)
			}
			var results struct{ Matches []model.Paper }
			if err := json.Unmarshal(out.Bytes(), &results); err != nil || results.Matches == nil {
				t.Fatalf("invalid lookup response %q: %v", out.String(), err)
			}
			var keys []string
			for _, paper := range results.Matches {
				keys = append(keys, paper.Key)
				if paper.Key == "PAPER001" && (paper.FirstAuthor != "Reed" || paper.Year != "2026" || paper.Artifact != "Quill") {
					t.Fatalf("missing disambiguation metadata: %+v", paper)
				}
			}
			if got := strings.Join(keys, " "); got != tc.keys {
				t.Fatalf("got %q, want %q", got, tc.keys)
			}
		})
	}
	for _, link := range []string{"zotero://select/groups/2/items/PAPER001", "https://www.zotero.org/someone-else/items/PAPER001", "https://api.zotero.org/users/555/items/PAPER001", "https://example.invalid/items/PAPER001"} {
		if err := run(context.Background(), []string{"find", link}, io.Discard); err == nil {
			t.Errorf("accepted foreign link: %s", link)
		}
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatal("read-only lookup created a state database", err)
	}
}

func TestRejectIncompatibleOrEmptySelectors(t *testing.T) {
	for _, args := range [][]string{
		{"sync", "--direction", "invalid"},
		{"sync", "--direction", ""},
		{"sync", "--item", ""},
		{"sync", "--backfill", ""},
		{"sync", "--direction", "both", "--item", "PAPER001"},
		{"sync", "--direction", "receive", "--backfill", "READ0001"},
	} {
		if _, err := parseOptions(args, config{Folder: "folder"}); err == nil {
			t.Errorf("accepted invalid sync arguments %q", args)
		}
	}
	if _, err := parseOptions([]string{"sync", "--direction", "receive"}, config{}); err == nil {
		t.Fatal("accepted receive-only sync with no folder")
	}
}
