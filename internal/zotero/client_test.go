package zotero

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func webClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{LibraryID: "12345", APIKey: "fake", WebDAVURL: "https://example.invalid/zotero/", WebDAVUsername: "user", WebDAVPassword: "pass", HelperPython: "/python", HelperPath: "/helper.py", LockDir: filepath.Join(t.TempDir(), "locks")})
	if err != nil {
		t.Fatal(err)
	}
	c.http = server.Client()
	c.base = server.URL
	return c
}

func TestMembersFollowsPaginationAndNestedCollections(t *testing.T) {
	c := webClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Zotero-API-Key") != "fake" || r.Header.Get("Zotero-API-Version") != "3" {
			t.Error("missing API headers")
		}
		w.Header().Set("Last-Modified-Version", "19")
		switch r.URL.Path {
		case "/collections/ROOT0001/collections":
			fmt.Fprint(w, `[{"key":"LEAF0001"}]`)
		case "/collections/ROOT0001/items/top":
			w.Header().Set("Total-Results", "2")
			switch r.URL.Query().Get("start") {
			case "0":
				fmt.Fprint(w, `[{"key":"PAPER001","data":{"itemType":"journalArticle","title":"A"}}]`)
			case "1":
				fmt.Fprint(w, `[{"key":"NOTES001","data":{"itemType":"note"}}]`)
			default:
				t.Error("unexpected Zotero offset")
				http.Error(w, "unexpected offset", 400)
			}
		case "/collections/LEAF0001/collections":
			fmt.Fprint(w, "[]")
		case "/collections/LEAF0001/items/top":
			fmt.Fprint(w, `[{"key":"PAPER001","data":{"itemType":"journalArticle","title":"A"}},{"key":"PAPER002","data":{"itemType":"conferencePaper","title":"B"}}]`)
		default:
			http.NotFound(w, r)
		}
	})
	items, err := c.Members(context.Background(), "ROOT0001")
	if err != nil || len(items) != 2 || items[0].Key != "PAPER001" || items[1].Key != "PAPER002" {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestPaperUsesOrderedAuthorsAndPublicationMetadata(t *testing.T) {
	for _, tc := range []struct {
		title, json, author, year string
	}{
		{"paper", `{"meta":{"parsedDate":"2026-05-12"},"data":{"date":"online 2025","shortTitle":"Quill","abstractNote":"We present QuillIndex (Quill for short), a catalog index.","creators":[{"creatorType":"editor","lastName":"Editor"},{"creatorType":"author","lastName":"Reed","firstName":"Avery"},{"creatorType":"author","lastName":"Vale"}]}}`, "Reed", "2026"},
		{"organization", `{"data":{"date":"Aug 2024","creators":[{"creatorType":"author","name":"Lab Group"}]}}`, "Lab Group", "2024"},
		{"undated", `{"data":{"dateAdded":"2024-02-04","date":"1999\u20132001","creators":[{"creatorType":"editor","lastName":"Editor"}]}}`, "", ""},
	} {
		t.Run(tc.title, func(t *testing.T) {
			var row record
			if err := json.Unmarshal([]byte(tc.json), &row); err != nil {
				t.Fatal(err)
			}
			p := row.paper()
			if p.FirstAuthor != tc.author || p.Year != tc.year {
				t.Fatalf("paper = %+v", p)
			}
			if tc.title == "paper" && (p.ShortTitle != "Quill" || p.Artifact != "Quill") {
				t.Fatalf("lost artifact metadata: %+v", p)
			}
		})
	}
}

func TestInconsistentPagesDoNotInitializeBaseline(t *testing.T) {
	c := webClient(t, func(w http.ResponseWriter, r *http.Request) {
		version := "1"
		if strings.HasSuffix(r.URL.Path, "/collections") {
			version = "2"
		}
		w.Header().Set("Last-Modified-Version", version)
		fmt.Fprint(w, "[]")
	})
	items, err := c.Members(context.Background(), "ROOT0001")
	if err == nil || items != nil {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestSourcesFiltersStoredPDFs(t *testing.T) {
	c := webClient(t, func(w http.ResponseWriter, r *http.Request) {
		type Row struct {
			Key  string            `json:"key"`
			Data map[string]string `json:"data"`
		}
		var entries []Row
		for i, p := range [][3]string{{"source.pdf", "imported_file", "application/pdf"}, {"download.pdf", "imported_url", "application/pdf"}, {"source2.pdf", "linked_file", "application/pdf"}, {"Kindle Notes - " + strings.Repeat("a", 64) + ".pdf", "imported_file", "application/pdf"}, {"page.html", "imported_url", "text/html"}} {
			entries = append(entries, Row{fmt.Sprintf("ATTCH%03d", i), map[string]string{"itemType": "attachment", "parentItem": "PAPER001", "filename": p[0], "linkMode": p[1], "contentType": p[2]}})
		}
		json.NewEncoder(w).Encode(entries)
	})
	found, err := c.Sources(context.Background(), "PAPER001")
	if err != nil || len(found) != 2 {
		t.Fatalf("candidates=%v err=%v", found, err)
	}
}

func TestValidateParentRejectsDeletedChildAndMismatchedRecords(t *testing.T) {
	for _, tc := range []struct {
		data string
		ok   bool
	}{
		{`"key":"PAPER001","itemType":"conferencePaper"`, true},
		{`"key":"PAPER001","itemType":"journalArticle","deleted":1`, false},
		{`"key":"PAPER001","itemType":"attachment"`, false},
		{`"key":"PAPER002","itemType":"journalArticle"`, false},
		{`"itemType":"journalArticle"`, false},
	} {
		c := webClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/items/PAPER001" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"key":"PAPER001","data":{%s}}`, tc.data)
		})
		if err := c.ValidateParent(context.Background(), "PAPER001"); (err == nil) != tc.ok {
			t.Errorf("expected ok=%t for %s, got %v", tc.ok, tc.data, err)
		}
	}
}
