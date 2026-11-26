package engine_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"kindle-sync/internal/engine"
	"kindle-sync/internal/model"
)

type retainedChild struct {
	parent, data string
	trashed      bool
}

type retentionLibrary struct {
	*fakeLibrary
	children                 map[string]retainedChild
	failArchive              bool
	failAfterTrash           bool
	created, retirementCalls int
}

func (z *retentionLibrary) Archive(_ context.Context, parent, path, child string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if child == "" {
		z.created++
		child = fmt.Sprintf("NOTE%04d", z.created)
	} else if existing := z.children[child]; existing.parent != parent || existing.trashed {
		return "", errors.New("attempted to reuse a missing or trashed child")
	}
	z.children[child] = retainedChild{parent: parent, data: string(data)}
	if z.failArchive {
		z.failArchive = false
		return child, errors.New("replacement verification interrupted")
	}
	return child, nil
}

func (z *retentionLibrary) RetireNotes(_ context.Context, parent string, keep, stale model.Receipt) error {
	z.retirementCalls++
	current, old := z.children[keep.Child], z.children[stale.Child]
	if keep.Child == stale.Child || current.parent != parent || old.parent != parent || current.trashed ||
		hashNotes(current.data) != keep.Hash || hashNotes(old.data) != stale.Hash {
		return errors.New("unsafe retirement request")
	}
	old.trashed = true
	z.children[stale.Child] = old
	if z.failAfterTrash {
		z.failAfterTrash = false
		return errors.New("lost remote trash acknowledgement")
	}
	return nil
}

func hashNotes(data string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(data))) }

type retentionFixture struct {
	*fixture
	remote *retentionLibrary
}

func setupRetention(t *testing.T, parents ...string) retentionFixture {
	t.Helper()
	f := newFixture(t)
	f.o.Direction = "receive"
	for _, parent := range parents {
		err := f.s.PutJob(model.Job{Paper: model.Paper{Key: parent}, Source: "SOURCE01", State: "submitted",
			OutboundName: parent + ".pdf", NamedAt: "2026-01-01T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
	}
	return retentionFixture{f, &retentionLibrary{fakeLibrary: f.z, children: map[string]retainedChild{}}}
}

func (f retentionFixture) sync(t *testing.T, expectProblem bool) {
	t.Helper()
	report, err := engine.Run(context.Background(), f.s, f.remote, f.g, f.o)
	if (err != nil || len(report.Problems) != 0) != expectProblem {
		t.Fatalf("unexpected sync outcome: %+v, %v", report, err)
	}
}

func exportAt(parent string, day int) model.DriveFile {
	return model.DriveFile{ID: fmt.Sprintf("%s-%d", parent, day), Version: "1", Name: parent + ".pdf",
		CreatedTime: fmt.Sprintf("2026-01-%02dT00:00:00Z", day)}
}

func (f retentionFixture) presentExport(parent string, day int, pdf string) {
	file := exportAt(parent, day)
	f.g.files = []model.DriveFile{file}
	f.g.bytes[file.ID] = pdf
}

func (f retentionFixture) requireLive(t *testing.T, parent string, data ...string) {
	t.Helper()
	var got []string
	for _, child := range f.remote.children {
		if child.parent == parent && !child.trashed {
			got = append(got, child.data)
		}
	}
	sort.Strings(got)
	sort.Strings(data)
	if !reflect.DeepEqual(got, data) {
		t.Fatalf("live attachments for %s: got %q, want %q", parent, got, data)
	}
}

func TestOnlyNewestNotesRemainAfterDuplicateLateAndRevertedExports(t *testing.T) {
	const parent, original, unrelated = "PAPER001", "%PDF-paper", "%PDF-personal"
	f := setupRetention(t, parent)
	f.remote.children["SOURCE01"] = retainedChild{parent: parent, data: original}
	f.remote.children["OTHER001"] = retainedChild{parent: parent, data: unrelated}
	for _, step := range []struct {
		day       int
		pdf, want string
	}{{2, "%PDF-A", "%PDF-A"}, {3, "%PDF-A", "%PDF-A"}, {5, "%PDF-B", "%PDF-B"},
		{4, "%PDF-older", "%PDF-B"}, {6, "%PDF-A", "%PDF-A"}} {
		f.presentExport(parent, step.day, step.pdf)
		f.sync(t, false)
		f.requireLive(t, parent, original, unrelated, step.want)
	}
}

func TestLastVerifiedNotesSurviveArchiveAndRetirementInterruptions(t *testing.T) {
	const parent = "PAPER001"
	f := setupRetention(t, parent)
	f.presentExport(parent, 2, "%PDF-before")
	f.sync(t, false)
	f.presentExport(parent, 3, "%PDF-after")
	f.remote.failArchive = true
	f.sync(t, true)
	if old := f.remote.children["NOTE0001"]; f.remote.retirementCalls != 0 || old.data != "%PDF-before" || old.trashed {
		t.Fatal("lost the previous attachment before verifying its replacement")
	}
	f.remote.failAfterTrash = true
	f.g.files = nil // The cached revision is still available to finish archiving.
	f.sync(t, true)
	f.requireLive(t, parent, "%PDF-after")
	f.sync(t, false)
	runs := f.remote.retirementCalls
	f.sync(t, false)
	if f.remote.created != 2 || f.remote.retirementCalls != runs {
		t.Fatalf("settled rescan repeated a write: created=%d retire calls=%d (was %d)", f.remote.created, f.remote.retirementCalls, runs)
	}
	f.requireLive(t, parent, "%PDF-after")
}

func TestExistingArchivesConvergeWithinRequestedScope(t *testing.T) {
	const a, b = "PAPER001", "PAPER002"
	f := setupRetention(t, a, b)
	for parentIndex, parent := range []string{a, b} {
		for _, day := range []int{2, 3} {
			file := exportAt(parent, day)
			data := fmt.Sprintf("%%PDF-day-%d", day)
			key := fmt.Sprintf("BACK%02d%02d", parentIndex, day)
			rec := model.Receipt{DriveFile: file, Parent: parent, State: "archived", Hash: hashNotes(data), Child: key}
			if err := f.s.PutReceipt(rec); err != nil {
				t.Fatal(err)
			}
			f.remote.children[key] = retainedChild{parent: parent, data: data}
		}
	}
	f.o.DryRun = true
	f.sync(t, false)
	f.o.DryRun = false
	f.o.Direction = "send"
	f.sync(t, false)
	if f.remote.retirementCalls != 0 {
		t.Fatal("preview or send-only command trashed a receipt")
	}
	f.presentExport(a, 3, "%PDF-day-3")
	f.o.Direction, f.o.ReturnFile = "receive", f.g.files[0].ID
	f.sync(t, false)
	f.requireLive(t, b, "%PDF-day-2", "%PDF-day-3")
	f.g.files, f.o.ReturnFile = nil, ""
	f.sync(t, false)
	f.requireLive(t, a, "%PDF-day-3")
	f.requireLive(t, b, "%PDF-day-3")
	if f.g.downloads != 0 || f.remote.created != 0 {
		t.Fatal("cleaning up existing archives created or downloaded new PDFs")
	}
}

func TestLegacyDatesHydratedFromMatchingDriveRevision(t *testing.T) {
	const parent = "PAPER001"
	f := setupRetention(t, parent)
	current := exportAt(parent, 9)
	rec := model.Receipt{DriveFile: current, Parent: parent, State: "archived", Hash: hashNotes("%PDF-latest"), Child: "NOTES009"}
	rec.CreatedTime = "" // Early receipts did not persist the Drive timestamp.
	if err := f.s.PutReceipt(rec); err != nil {
		t.Fatal(err)
	}
	f.remote.children[rec.Child] = retainedChild{parent: parent, data: "%PDF-latest"}
	f.presentExport(parent, 5, "%PDF-older")
	f.g.files = append(f.g.files, current)
	f.sync(t, false)
	f.requireLive(t, parent, "%PDF-latest")

	f.presentExport(parent, 7, "%PDF-still older") // Day 9 has left the Drive folder.
	f.sync(t, false)
	f.requireLive(t, parent, "%PDF-latest")
}

func TestUndatedLegacyArchivesWaitForKnownLaterExport(t *testing.T) {
	const parent = "PAPER001"
	f := setupRetention(t, parent)
	for index, data := range []string{"%PDF-legacy A", "%PDF-legacy B"} {
		file := exportAt(parent, index+2)
		file.CreatedTime = ""
		rec := model.Receipt{DriveFile: file, Parent: parent, State: "archived", Hash: hashNotes(data), Child: fmt.Sprintf("BACK%04d", index)}
		if err := f.s.PutReceipt(rec); err != nil {
			t.Fatal(err)
		}
		f.remote.children[rec.Child] = retainedChild{parent: parent, data: data}
	}
	f.sync(t, true) // Either missing Drive file could have contained the newer notes.
	f.requireLive(t, parent, "%PDF-legacy A", "%PDF-legacy B")
	f.presentExport(parent, 5, "%PDF-newer")
	f.g.files[0].CreatedTime = time.Now().UTC().Format(time.RFC3339Nano)
	f.sync(t, false)
	f.requireLive(t, parent, "%PDF-newer")
}

func TestNewDriveRevisionWithEarlierModifiedTime(t *testing.T) {
	const parent = "PAPER001"
	f := setupRetention(t, parent)
	f.presentExport(parent, 2, "%PDF-before")
	file := &f.g.files[0]
	file.ModifiedTime = "2026-01-08T00:00:00Z"
	f.sync(t, false)
	file.Version, file.ModifiedTime = "2", "2026-01-05T00:00:00Z"
	f.g.bytes[file.ID] = "%PDF-after"
	f.sync(t, false)
	f.requireLive(t, parent, "%PDF-after")
}

func TestLegacyBackdatedRevisionsAcrossDriveFiles(t *testing.T) {
	const parent = "PAPER001"
	f := setupRetention(t, parent)
	for i, row := range []struct {
		id, version, data string
		day               int
	}{{"x", "1", "%PDF-X1", 9}, {"x", "2", "%PDF-X2", 5}, {"y", "1", "%PDF-Y", 8}} {
		// The newest revision of X was backdated. Only X2 can compete with Y.
		file := exportAt(parent, 2)
		file.ID, file.Version = row.id, row.version
		file.ModifiedTime = fmt.Sprintf("2026-01-%02dT00:00:00Z", row.day)
		key := fmt.Sprintf("BACK%04d", i)
		rec := model.Receipt{DriveFile: file, Parent: parent, State: "archived", Hash: hashNotes(row.data), Child: key}
		if err := f.s.PutReceipt(rec); err != nil {
			t.Fatal(err)
		}
		f.remote.children[key] = retainedChild{parent: parent, data: row.data}
	}
	f.sync(t, false)
	f.requireLive(t, parent, "%PDF-Y")
}
