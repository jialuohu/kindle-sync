package zotero

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"kindle-sync/internal/model"
)

// Tests the packaged helper across its actual subprocess and HTTPS boundaries.
// Point KINDLE_SYNC_TEST_PYTHON to a venv built with helper/requirements.txt.
func TestFetchViaWebDAV(t *testing.T) {
	python := os.Getenv("KINDLE_SYNC_TEST_PYTHON")
	if python == "" {
		t.Skip("set KINDLE_SYNC_TEST_PYTHON to exercise the packaged Python runtime")
	}
	root := t.TempDir()
	pdf, err := exec.Command(python, "-I", "-c", "import sys,fitz; d=fitz.open(); d.new_page().insert_text((20,20),'Copyright 2023 IEEE'); sys.stdout.buffer.write(d.tobytes())").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", md5.Sum(pdf))
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for _, v := range []struct {
		name string
		body []byte
	}{{"source.pdf", pdf}, {"resources/icon.png", []byte{0}}} {
		file, err := z.Create(v.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(v.body); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pw, ok := r.BasicAuth()
		if !ok || user != "test" || pw != "password" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/zotero/ATTACH01.zip":
			w.Write(archive.Bytes())
		case "/zotero/ATTACH01.prop":
			fmt.Fprintf(w, "<properties><hash>%s</hash></properties>", hash)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	bundle := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REQUESTS_CA_BUNDLE", bundle)
	script, err := filepath.Abs("../../helper/zotero_attachment.py")
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{LibraryID: "1234", APIKey: "test", WebDAVURL: srv.URL + "/zotero/", WebDAVUsername: "test", WebDAVPassword: "password", HelperPython: python, HelperPath: script, LockDir: filepath.Join(root, "locks")})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "prepared.pdf")
	a := model.Attachment{Key: "ATTACH01", Name: "source.pdf"}
	if year, err := c.Download(context.Background(), a, dest); err != nil || year != "2023" {
		t.Fatalf("expected the printed year from the downloaded PDF, got %q, %v", year, err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatalf("incorrect fetched bytes: %v", err)
	}
	a.MD5 = strings.Repeat("0", 32)
	_, err = c.Download(context.Background(), a, dest)
	if err == nil || !strings.Contains(err.Error(), "checksum_mismatch") {
		t.Fatalf("wrong digest accepted: %v", err)
	}
	got, err = os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatal("existing destination damaged")
	}
}

func TestArchiveReturnsPartialKey(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for subprocess tests")
	}
	d := t.TempDir()
	script := filepath.Join(d, "receipt.py")
	code := `import json,sys
print(json.dumps({"status":"incomplete","backend":"webdav","parent_key":"PARENT01","attachment_key":"ATTACH01","error":"webdav_upload_failed"}))
sys.exit(1)`
	if err := os.WriteFile(script, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Client{config: Config{HelperPython: python, HelperPath: script}}
	child, err := c.Archive(context.Background(), "PARENT01", filepath.Join(d, "stable.pdf"), "")
	if err == nil || child != "ATTACH01" {
		t.Fatalf("partial child=%s err=%v", child, err)
	}
}

// The metadata client is in-memory; file transfer, sidecar creation, PDF parsing,
// post-upload verification, and reading from a fresh client use production code.
func TestArchiveViaWebDAV(t *testing.T) {
	python := os.Getenv("KINDLE_SYNC_TEST_PYTHON")
	if python == "" {
		t.Skip("set KINDLE_SYNC_TEST_PYTHON to exercise the packaged Python runtime")
	}
	root := t.TempDir()
	pdf, err := exec.Command(python, "-I", "-c", "import sys,fitz; d=fitz.open(); d.new_page().insert_text((20,20),'Marked document'); sys.stdout.buffer.write(d.tobytes())").Output()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("Kindle Notes - %x.pdf", sha256.Sum256(pdf))
	source := filepath.Join(root, name)
	if err = os.WriteFile(source, pdf, 0600); err != nil {
		t.Fatal(err)
	}

	objects := map[string][]byte{}
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pw, ok := r.BasicAuth()
		if !ok || user != "reader" || pw != "fake" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PROPFIND":
			if r.URL.Path != "/dav/" || r.Header.Get("Depth") != "0" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(207)
			io.WriteString(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
		case "PUT":
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "bad body", 400)
				return
			}
			objects[r.URL.Path] = body
			w.WriteHeader(201)
		case "GET":
			if body, ok := objects[r.URL.Path]; ok {
				w.Write(body)
			} else {
				http.NotFound(w, r)
			}
		default:
			w.WriteHeader(405)
		}
	}))
	defer srv.Close()
	bundle := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REQUESTS_CA_BUNDLE", bundle)
	script, err := filepath.Abs("../../helper/zotero_attachment.py")
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "inject_test_metadata.py")
	stub := fmt.Sprintf("import sys\nsys.path.insert(0, %q)\nfrom test_attachment import MODULE, FakeClient\nMODULE._new_client = lambda env: FakeClient()\nraise SystemExit(MODULE.main())\n", filepath.Join(filepath.Dir(script), "tests"))
	if err = os.WriteFile(fake, []byte(stub), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{LibraryID: "1234", APIKey: "test", WebDAVURL: srv.URL + "/dav/", WebDAVUsername: "reader", WebDAVPassword: "fake", HelperPython: python, HelperPath: fake, LockDir: filepath.Join(root, "locks")}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.Archive(context.Background(), "PARENT01", source, "")
	if child != "ATTACH01" || err != nil {
		t.Fatalf("archive child=%q err=%v", child, err)
	}
	mu.Lock()
	prop := objects["/dav/ATTACH01.prop"]
	zipped := objects["/dav/ATTACH01.zip"]
	mu.Unlock()
	if !bytes.Contains(prop, []byte(fmt.Sprintf("<hash>%x</hash>", md5.Sum(pdf)))) {
		t.Fatalf("invalid .prop: %q", prop)
	}
	archive, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil || len(archive.File) != 1 || archive.File[0].Name != name {
		t.Fatalf("invalid archive member, err=%v", err)
	}
	cfg.HelperPath = script
	fresh, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "fresh.pdf")
	if _, err = fresh.Download(context.Background(), model.Attachment{Key: child, Name: name}, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatalf("fresh WebDAV readback mismatch: %v", err)
	}
}
