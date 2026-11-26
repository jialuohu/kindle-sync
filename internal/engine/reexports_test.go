package engine_test

import (
	"strings"
	"testing"
	"time"

	"kindle-sync/internal/model"
)

func TestRepeatExportAcrossDriveFilesAndRevisions(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("PAPER001", "%PDF-origin")
	f.run(t)
	name := strings.TrimSuffix(f.jobs(t)["PAPER001"].OutboundName, ".pdf") + " - Kindle.pdf"
	f.o.Direction = "receive"

	export := model.DriveFile{ID: "first-upload", Version: "1", Name: name}
	f.g.files = []model.DriveFile{export}
	f.g.bytes[export.ID] = "%PDF-first notes"
	f.run(t)
	f.run(t)
	if len(f.z.archives) != 1 || f.g.downloads != 1 {
		t.Fatalf("unchanged Drive revision: archives=%d downloads=%d", len(f.z.archives), f.g.downloads)
	}

	second := model.DriveFile{ID: "second-upload", Version: "1", Name: name}
	f.g.files = append(f.g.files, second)
	f.g.bytes[second.ID] = f.g.bytes[export.ID]
	f.run(t)
	if len(f.z.archives) != 1 {
		t.Fatalf("identical PDF uploaded under a new ID added an attachment: %+v", f.z.archives)
	}
	second.Version = "2"
	f.g.files[1] = second
	f.g.bytes[second.ID] = "%PDF-revised notes"
	f.run(t)
	downloads := f.g.downloads
	f.run(t)
	if len(f.z.archives) != 2 || f.g.downloads != downloads {
		t.Fatalf("rescanning the two exports: archives=%d downloads=%d (was %d)", len(f.z.archives), f.g.downloads, downloads)
	}
	firstNotes, revisedNotes := f.z.archives[0], f.z.archives[1]
	if firstNotes.parent != "PAPER001" || revisedNotes.parent != "PAPER001" || firstNotes.name == revisedNotes.name ||
		firstNotes.bytes != "%PDF-first notes" || revisedNotes.bytes != "%PDF-revised notes" || revisedNotes.previous != "" {
		t.Fatalf("expected two separate notes attachments under the same paper: %+v", f.z.archives)
	}

	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 3 {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
	hashes := map[[2]string]string{}
	for _, receipt := range receipts {
		wantState := "retired"
		if receipt.ID == second.ID && receipt.Version == "2" {
			wantState = "archived"
		}
		if receipt.Parent != "PAPER001" || receipt.State != wantState || receipt.Hash == "" {
			t.Fatalf("incomplete receipt: %+v", receipt)
		}
		hashes[[2]string{receipt.ID, receipt.Version}] = receipt.Hash
	}
	if len(f.z.retired) != 1 || f.z.retired[0] != "child-key" {
		t.Fatalf("older attachment retired %v", f.z.retired)
	}
	first := hashes[[2]string{export.ID, "1"}]
	if first != hashes[[2]string{second.ID, "1"}] || first == hashes[[2]string{second.ID, "2"}] {
		t.Fatalf("unexpected content hashes: %+v", hashes)
	}
}

func TestArchiveBatchByDriveModificationTimeAndNumericRevision(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("PAPER001", "%PDF-original")
	f.run(t)
	j := f.jobs(t)["PAPER001"]
	start, err := time.Parse(time.RFC3339Nano, j.NamedAt)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := func(offset time.Duration) string { return start.Add(offset).Format(time.RFC3339Nano) }
	first := model.DriveFile{
		ID: "a", Version: "2", Name: j.OutboundName,
		CreatedTime: timestamp(time.Minute), ModifiedTime: timestamp(3 * time.Minute),
	}
	f.o.Direction = "receive"
	f.g.files = []model.DriveFile{first}
	f.g.bytes[first.ID] = "%PDF-revision two"
	f.z.partial = true
	f.run(t) // This revision's bytes stay available while Drive replaces it.

	second := first
	second.Version = "10"
	earlier := model.DriveFile{ID: "z", Version: "1", Name: j.OutboundName, CreatedTime: timestamp(2 * time.Minute)}
	f.g.files = []model.DriveFile{second, earlier}
	f.g.bytes[second.ID] = "%PDF-revision ten"
	f.g.bytes[earlier.ID] = "%PDF-earlier export"
	r := f.run(t)
	if len(r.Problems) != 0 || len(f.z.archives) != 2 || len(f.z.retired) != 1 {
		t.Fatalf("archives=%+v retired=%v problems=%v", f.z.archives, f.z.retired, r.Problems)
	}
	if f.z.archives[1].bytes != "%PDF-revision ten" || f.g.downloads != 2 {
		t.Fatal("batch must retain the newest numeric revision and skip the earlier export")
	}
}
