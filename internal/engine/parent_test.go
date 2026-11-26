package engine_test

import (
	"context"
	"errors"
	"testing"

	"kindle-sync/internal/engine"
	"kindle-sync/internal/model"
)

type libraryWithParentError struct {
	*fakeLibrary
	err error
}

func (z libraryWithParentError) ValidateParent(context.Context, string) error { return z.err }

func TestReturnAfterLeavingReadingCollection(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("PAPER001", "%PDF-source")
	f.add("PAPER002", "") // Source arrives after the paper leaves ReadLater.
	f.run(t)
	f.z.collections["root"] = nil
	f.z.pdfs["source-PAPER002"] = "%PDF-arrived"
	f.g.files = []model.DriveFile{{ID: "export", Version: "1", Name: f.jobs(t)["PAPER001"].OutboundName}}
	f.g.bytes["export"] = "%PDF-ink"
	f.run(t)
	if len(f.g.sent) != 1 || len(f.z.archives) != 1 || f.z.archives[0].parent != "PAPER001" {
		t.Fatalf("sent=%v archives=%v", f.g.sent, f.z.archives)
	}
	f.z.collections["root"] = []model.Paper{{Key: "PAPER001"}}
	f.run(t)
	if len(f.g.sent) != 1 || len(f.z.archives) != 1 {
		t.Fatal("returning the paper to ReadLater repeated a delivery or an archive")
	}
}

func TestReturnWaitsForLiveParentBeforeDownloading(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		wantProblems int
	}{
		{"trashed or removed", model.ErrParentGone, 0},
		{"temporary failure", errors.New("Zotero unavailable"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.run(t)
			f.add("PAPER001", "%PDF-source")
			f.run(t)
			f.z.collections["root"] = nil
			f.g.files = []model.DriveFile{{ID: "export", Version: "1", Name: f.jobs(t)["PAPER001"].OutboundName}}
			f.g.bytes["export"] = "%PDF-ink"
			library := libraryWithParentError{f.z, tc.err}
			rep, err := engine.Run(context.Background(), f.s, library, f.g, f.o)
			if err != nil || len(rep.Problems) != tc.wantProblems {
				t.Fatal(rep, err)
			}
			f.o.ReturnFile = "export"
			if _, err = engine.Run(context.Background(), f.s, library, f.g, f.o); !errors.Is(err, tc.err) {
				t.Fatalf("targeted return should report the parent error, got %v", err)
			}
			receipts, err := f.s.Receipts()
			if err != nil || len(receipts) != 0 || f.g.downloads != 0 || len(f.z.archives) != 0 {
				t.Fatalf("receipts=%v downloads=%d archives=%v err=%v", receipts, f.g.downloads, f.z.archives, err)
			}
			f.o.ReturnFile = ""
			f.run(t) // A restored parent or recovered connection can receive the original export.
			if len(f.z.archives) != 1 || f.z.archives[0].parent != "PAPER001" || len(f.g.sent) != 1 {
				t.Fatal(f.z.archives, f.g.sent)
			}
		})
	}
}
