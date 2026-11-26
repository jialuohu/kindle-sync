package googleio

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"kindle-sync/internal/model"
)

func fakeClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	ctx := context.Background()
	opts := []option.ClientOption{option.WithEndpoint(server.URL + "/"), option.WithHTTPClient(server.Client())}
	gs, err := gmail.NewService(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := drive.NewService(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{config: Config{Sender: "test@example.com", KindleAddress: "device@kindle.com", FolderID: "export-folder"}, gmailService: func() (*gmail.Service, error) { return gs, nil }, driveService: func() (*drive.Service, error) { return ds, nil }}
}

func TestSendUploadsOneRealAttachment(t *testing.T) {
	content := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{1, 88, 255, 245}, 140)...)
	filename := "KRS-5432 - \u591a\u9875.pdf"
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/upload/gmail/v1/users/me/messages/send" || r.URL.Query().Get("uploadType") != "multipart" {
			t.Error("unexpected send URL", r.URL)
		}
		contentType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "multipart/related" {
			t.Error("invalid upload envelope")
			return
		}
		outer := multipart.NewReader(r.Body, params["boundary"])
		_, err = outer.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		part, err := outer.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		if part.Header.Get("Content-Type") != "message/rfc822" {
			t.Error("invalid message media type")
		}
		msg, err := mail.ReadMessage(part)
		if err != nil {
			t.Error(err)
			return
		}
		subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
		if err != nil || subject != "KRS-5432 - \u591a\u9875" || msg.Header.Get("From") != "test@example.com" || msg.Header.Get("To") != "device@kindle.com" {
			t.Error("invalid envelope or lost token")
		}
		_, params, err = mime.ParseMediaType(msg.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			return
		}
		pieces := multipart.NewReader(msg.Body, params["boundary"])
		att, err := pieces.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		if att.FileName() != filename || att.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Error("invalid attachment", att.Header)
		}
		body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, att))
		if err != nil || !bytes.Equal(body, content) {
			t.Error("PDF bytes changed")
		}
		if _, err := pieces.NextPart(); err != io.EOF {
			t.Error("expected exactly one PDF")
		}
		io.WriteString(w, `{"id":"provider-message"}`)
	})
	src := filepath.Join(t.TempDir(), "sample.pdf")
	if err := os.WriteFile(src, content, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := c.Send(context.Background(), src, filename)
	if err != nil || id != "provider-message" {
		t.Fatalf("send = %q, %v", id, err)
	}
}

func TestSendOutcomeClassification(t *testing.T) {
	src := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(src, []byte("some content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{200, "{}", model.ErrUncertain}, {200, "bad response", model.ErrUncertain},
		{500, "{}", model.ErrUncertain}, {408, "{}", model.ErrUncertain},
		{400, "{}", model.ErrSendRejected}, {403, "{}", model.ErrSendRejected}, {429, "{}", model.ErrSendRejected},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			var count atomic.Int32
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, err := c.Send(context.Background(), src, "safe.pdf")
			if !errors.Is(err, tc.want) || count.Load() != 1 {
				t.Fatalf("send error %v, calls %d", err, count.Load())
			}
			_, err = c.Send(context.Background(), src, "bad\r\nInjected: header.pdf")
			if !errors.Is(err, model.ErrSendRejected) || count.Load() != 1 {
				t.Fatal("sent invalid filename")
			}
		})
	}
}

func TestDriveFolderPaginationAndVerifiedDownload(t *testing.T) {
	pdf := []byte("%PDF-1.7\n")
	sum := md5.Sum(pdf)
	digest := hex.EncodeToString(sum[:])
	var move atomic.Bool
	var corrupt atomic.Bool
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files":
			if !strings.Contains(r.URL.Query().Get("q"), "'export-folder' in parents") {
				t.Error("missing folder restriction")
			}
			if r.URL.Query().Get("pageToken") == "" {
				io.WriteString(w, `{"nextPageToken":"next","files":[]}`)
			} else {
				fmt.Fprintf(w, `{"files":[{"id":"A","name":"mark.pdf","version":"7","md5Checksum":%q}]}`, digest)
			}
		case "/files/export-folder":
			io.WriteString(w, `{"mimeType":"application/vnd.google-apps.folder","trashed":false}`)
		case "/files/A":
			if r.URL.Query().Get("alt") == "media" {
				if corrupt.Load() {
					w.Write(bytes.Repeat([]byte("X"), len(pdf)))
				} else {
					w.Write(pdf)
				}
				return
			}
			parent := "export-folder"
			if move.Load() {
				parent = "another-folder"
			}
			fmt.Fprintf(w, `{"id":"A","name":"mark.pdf","parents":[%q],"mimeType":"application/pdf","version":"7","size":"%d","md5Checksum":%q}`, parent, len(pdf), digest)
		default:
			t.Error("unexpected URL", r.URL)
			http.NotFound(w, r)
		}
	})
	files, err := c.Files(context.Background())
	if err != nil || len(files) != 1 || files[0].Version != "7" {
		t.Fatalf("list = %+v, %v", files, err)
	}
	dst := filepath.Join(t.TempDir(), "download.pdf")
	if err := c.Download(context.Background(), files[0], dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatal("unexpected download")
	}
	corrupt.Store(true)
	if err := c.Download(context.Background(), files[0], dst); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	move.Store(true)
	if err := c.Download(context.Background(), files[0], dst); err == nil {
		t.Fatal("foreign parent accepted")
	}
	got, err = os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatal("bad download replaced valid file")
	}
}

func TestDeployAccountBoundToPrivateTokens(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{SecretsDir: dir, Sender: "chosen@example.com"}
	body := []byte(`{"installed":{"client_id":"client","client_secret":"x","redirect_uris":["http://localhost"]}}`)
	if err := os.WriteFile(filepath.Join(dir, "google-client.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	g, err := oauthConfig(cfg, "gmail")
	if err != nil {
		t.Fatal(err)
	}
	d, err := oauthConfig(cfg, "drive")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Scopes) != 2 || g.Scopes[0] != gmail.GmailSendScope || len(d.Scopes) != 1 || d.Scopes[0] != drive.DriveReadonlyScope {
		t.Fatal("unexpected OAuth scopes")
	}
	rec := credentials{Service: "gmail", ClientID: "client", Email: "another@example.com", Token: oauth2.Token{RefreshToken: "placeholder"}}
	path := tokenPath(cfg, "gmail")
	if err := saveCredentials(path, rec); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("token must be private")
	}
	if _, err := authorizedClient(context.Background(), cfg, "gmail"); err == nil {
		t.Fatal("accepted mismatched sender")
	}
	rec.Email = cfg.Sender
	if err := saveCredentials(path, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizedClient(context.Background(), cfg, "gmail"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !json.Valid(b) {
		t.Fatal("invalid persisted credentials")
	}
}

func TestRefreshPersistsRotationAndPreservesNewAuthorization(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "first" {
			t.Error("unexpected grant")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"active","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	cfg := &oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{TokenURL: server.URL}}
	path := filepath.Join(t.TempDir(), "token.json")
	rec := credentials{Service: "gmail", ClientID: cfg.ClientID, Email: "first@example.com", Token: oauth2.Token{RefreshToken: "first"}}
	if err := saveCredentials(path, rec); err != nil {
		t.Fatal(err)
	}
	src := &tokenCache{ctx: ctx, cfg: cfg, path: path, record: rec}
	for range 2 {
		tok, err := src.Token()
		if err != nil || tok.AccessToken != "active" || tok.RefreshToken != "rotated" {
			t.Fatalf("failed to cache refreshed token: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate token refreshes")
	}
	rec.Email = "replacement@example.com"
	if err := withTokenLock(path, func() error { return saveCredentials(path, rec) }); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Token(); err == nil || calls.Load() != 1 {
		t.Fatal("stale worker reused a new account's grant")
	}
	after, err := readCredentials(path, "gmail", "client")
	if err != nil || after.Email != rec.Email {
		t.Fatal("stale worker overwrote new grant")
	}
}
