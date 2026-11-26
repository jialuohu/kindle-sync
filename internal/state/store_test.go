package state

import (
	"kindle-sync/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestDurableQueueAndLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	readonly, err := Open(dir, "id", false)
	if err != nil {
		t.Fatal(err)
	}
	readonly.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("read-only preview created state")
	}
	w, err := Open(dir, "id", true)
	if err != nil {
		t.Fatal(err)
	}
	if w2, err := Open(dir, "id", true); err == nil {
		w2.Close()
		t.Fatal("double writer")
	}
	if err := w.Seed([]model.Paper{{Key: "key", Title: "t"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Seed(nil); err == nil {
		t.Fatal("accepted second baseline")
	}
	if err := w.PutJob(model.Job{Paper: model.Paper{Key: "key"}, State: "sending"}); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2", "2"} {
		if err := w.PutReceipt(model.Receipt{DriveFile: model.DriveFile{ID: "f", Version: version}, State: "archived"}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(dir, "id", false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.Close()
	j, err := r.Jobs()
	if err != nil || len(j) != 1 || j[0].State != "sending" {
		t.Fatal(j, err)
	}
	rec, err := r.Receipts()
	if err != nil || len(rec) != 2 {
		t.Fatal(rec, err)
	}
	if yes, err := r.Initialized(); err != nil || !yes {
		t.Fatal(yes, err)
	}
	if w2, err := Open(dir, "another", true); err == nil {
		w2.Close()
		t.Fatal("accepted wrong scope")
	}
}
