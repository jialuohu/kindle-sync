package zotero

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kindle-sync/internal/model"
)

func TestRetireNotes(t *testing.T) {
	const parent = "PARENT01"
	keep := model.Receipt{Hash: strings.Repeat("a", 64), Parent: parent, State: "archived", Child: "LATEST01"}
	keep.MD5 = strings.Repeat("b", 32)
	stale := model.Receipt{Hash: strings.Repeat("c", 64), Parent: parent, State: "archived", Child: "OLDER001"}
	stale.MD5 = strings.Repeat("d", 32)
	type itemData = map[string]any
	attachment := func(r model.Receipt) itemData {
		return itemData{"key": r.Child, "itemType": "attachment", "parentItem": parent, "linkMode": "imported_file",
			"contentType": "application/pdf", "filename": "Kindle Notes - " + r.Hash + ".pdf", "md5": r.MD5}
	}
	for _, tc := range []struct {
		name           string
		edit           func(map[string]itemData)
		writeStatus    int
		readbackActive bool
		itemFailed     bool
		wantWrites     int
		wantError      bool
	}{
		{name: "replace", wantWrites: 1},
		{name: "already trashed", edit: func(m map[string]itemData) { m[stale.Child]["deleted"] = 1 }},
		{name: "already removed", edit: func(m map[string]itemData) { delete(m, stale.Child) }},
		{name: "parent in trash", edit: func(m map[string]itemData) { m[parent]["deleted"] = 1 }, wantError: true},
		{name: "new attachment missing", edit: func(m map[string]itemData) { delete(m, keep.Child) }, wantError: true},
		{name: "new attachment trashed", edit: func(m map[string]itemData) { m[keep.Child]["deleted"] = true }, wantError: true},
		{name: "new data changed", edit: func(m map[string]itemData) { m[keep.Child]["md5"] = stale.MD5 }, wantError: true},
		{name: "old data changed", edit: func(m map[string]itemData) { m[stale.Child]["md5"] = keep.MD5 }, wantError: true},
		{name: "original PDF", edit: func(m map[string]itemData) { m[stale.Child]["filename"] = "original.pdf" }, wantError: true},
		{name: "wrong paper", edit: func(m map[string]itemData) { m[stale.Child]["parentItem"] = "PARENT02" }, wantError: true},
		{name: "linked PDF", edit: func(m map[string]itemData) { m[stale.Child]["linkMode"] = "linked_file" }, wantError: true},
		{name: "concurrent change", writeStatus: 412, wantWrites: 1, wantError: true},
		{name: "failed item in batch", itemFailed: true, wantWrites: 1, wantError: true},
		{name: "readback still live", readbackActive: true, wantWrites: 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := map[string]itemData{parent: {"key": parent, "itemType": "book"}, keep.Child: attachment(keep), stale.Child: attachment(stale)}
			if tc.edit != nil {
				tc.edit(items)
			}
			version := 102
			writes := 0
			c := webClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/items" || r.Header.Get("Zotero-API-Key") != "fake" || r.Header.Get("Zotero-API-Version") != "3" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				switch r.Method {
				case "GET":
					q := r.URL.Query()
					if q.Get("itemKey") != parent+","+keep.Child+","+stale.Child || q.Get("includeTrashed") != "1" || q.Get("limit") != "3" {
						t.Error("incorrect item selection")
					}
					rows := []map[string]any{}
					for key, data := range items {
						rows = append(rows, map[string]any{"key": key, "version": 2, "data": data})
					}
					w.Header().Set("Last-Modified-Version", fmt.Sprint(version))
					w.Header().Set("Total-Results", fmt.Sprint(len(rows)))
					json.NewEncoder(w).Encode(rows)
				case "POST":
					writes++
					body, _ := io.ReadAll(r.Body)
					if string(body) != `[{"key":"OLDER001","deleted":1}]` || r.Header.Get("If-Unmodified-Since-Version") != fmt.Sprint(version) {
						t.Errorf("wrong conditional update %s", body)
					}
					if tc.writeStatus != 0 {
						w.WriteHeader(tc.writeStatus)
						return
					}
					if tc.itemFailed {
						fmt.Fprint(w, `{"failed":{"0":{"code":412}}}`)
						return
					}
					if !tc.readbackActive {
						items[stale.Child]["deleted"] = 1
					}
					version++
					fmt.Fprint(w, `{"successful":{"0":{"key":"OLDER001"}},"failed":{}}`)
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(405)
				}
			})
			err := c.RetireNotes(context.Background(), parent, keep, stale)
			if tc.name == "parent in trash" && !errors.Is(err, model.ErrParentGone) {
				t.Fatal(err)
			}
			if (err != nil) != tc.wantError || writes != tc.wantWrites {
				t.Fatalf("writes=%d err=%v", writes, err)
			}
			if !tc.wantError {
				if err := c.RetireNotes(context.Background(), parent, keep, stale); err != nil || writes != tc.wantWrites {
					t.Fatalf("retry writes=%d err=%v", writes, err)
				}
			}
		})
	}
}

func TestRetireNotesRejectsInvalidReceiptsBeforeNetwork(t *testing.T) {
	c := webClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected network call") })
	keep := model.Receipt{Parent: "PARENT01", Child: "CHILD001", Hash: strings.Repeat("a", 64), State: "archived"}
	for _, change := range []func(*model.Receipt){
		func(s *model.Receipt) { s.Child = keep.Child }, func(s *model.Receipt) { s.Child = keep.Parent },
		func(s *model.Receipt) { s.Parent = "PARENT02" }, func(s *model.Receipt) { s.Hash = "invalid" },
		func(s *model.Receipt) { s.MD5 = "invalid" },
	} {
		stale := keep
		stale.Child = "CHILD002"
		change(&stale)
		if err := c.RetireNotes(context.Background(), keep.Parent, keep, stale); err == nil {
			t.Fatal("accepted invalid receipt")
		}
	}
}

func TestVerifyNotesChecksStoredBytesWithoutReceiptMD5(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("requires python3 for helper fixture")
	}
	path := filepath.Join(t.TempDir(), "fetch.py")
	if err := os.WriteFile(path, []byte(`
import hashlib, json, pathlib, sys
pdf = b"%PDF-handwriting"
pathlib.Path(sys.argv[sys.argv.index("--output") + 1]).write_bytes(pdf)
print(json.dumps({"attachment_key":"NOTES001", "status":"downloaded", "md5":hashlib.md5(pdf).hexdigest()}))
`), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Client{config: Config{HelperPython: python, HelperPath: path}}
	for _, tc := range []struct {
		contents string
		ok       bool
	}{{"%PDF-handwriting", true}, {"%PDF-user-edit", false}} {
		r := model.Receipt{Parent: "PARENT01", Child: "NOTES001", State: "archived", Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(tc.contents)))}
		var row record
		row.Data.Parent, row.Data.ItemType, row.Data.LinkMode, row.Data.ContentType = r.Parent, "attachment", "imported_file", "application/pdf"
		row.Data.Filename = "Kindle Notes - " + r.Hash + ".pdf"
		row.Data.MD5 = fmt.Sprintf("%x", md5.Sum([]byte("%PDF-handwriting")))
		err := c.verifyNotes(context.Background(), row, &r)
		if (err == nil) != tc.ok || (r.MD5 != "") != tc.ok {
			t.Fatalf("verified=%v error=%v", r.MD5 != "", err)
		}
	}
}
