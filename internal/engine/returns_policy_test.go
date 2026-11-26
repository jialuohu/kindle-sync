package engine_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"kindle-sync/internal/engine"
	"kindle-sync/internal/model"
)

// Seed the on-disk format used by releases that cached unassigned exports.
func seedLegacyHeld(t *testing.T, f *fixture, file model.DriveFile, contents string) {
	t.Helper()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
	dir := filepath.Join(f.o.Spool, "returns", fmt.Sprintf("%x", sha256.Sum256([]byte(file.ID+"\x00"+file.Version))))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Kindle Notes - "+hash+".pdf"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutReceipt(model.Receipt{DriveFile: file, State: "needs_match", Hash: hash}); err != nil {
		t.Fatal(err)
	}
}

func TestUnmatchedExportsStayOnlyOnDrive(t *testing.T) {
	f := newFixture(t)
	f.o.Direction = "receive"
	const token = "KRS-ABCDEFGHIJKLMNOP"
	const sent = "2025-06-01T12:00:00Z"
	for _, tc := range []struct{ key, state, token, named string }{
		{"paper", "submitted", "", sent},
		{"staged", "manual_upload", "", sent},
		{"old", "baseline", token, ""},
		{"queued", "queued", "", sent},
		{"interrupted", "sending", "", sent},
		{"uncertain", "uncertain", "", sent},
		{"no-time", "submitted", "", ""},
	} {
		j := model.Job{Paper: model.Paper{Key: tc.key}, State: tc.state, Token: tc.token, OutboundName: tc.key + ".pdf", NamedAt: tc.named}
		if err := f.s.PutJob(j); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"budget - Kindle.pdf", "paper appendix - Kindle.pdf", "staged - Kindle.pdf", token + " Old.pdf", "queued.pdf", "interrupted.pdf", "uncertain.pdf", "no-time.pdf"} {
		f.g.files = append(f.g.files, model.DriveFile{ID: name, Name: name, Version: "1", CreatedTime: "2026-01-01T00:00:00Z"})
	}
	f.g.files = append(f.g.files, model.DriveFile{ID: "predates", Name: "paper - Kindle.pdf", Version: "1", CreatedTime: "2025-05-31T23:00:00Z"})
	for _, dry := range []bool{true, false, false} {
		f.o.DryRun = dry
		if report := f.run(t); len(report.Actions)+len(report.Problems) != 0 {
			t.Fatal(report)
		}
	}
	f.o.ReturnFile = "predates"
	if report := f.run(t); len(report.Actions) != 1 || len(report.Problems) != 0 {
		t.Fatal("targeted receive should explain the skip", report)
	}
	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 0 || f.g.downloads != 0 || f.z.validations != 0 || len(f.z.archives) != 0 {
		t.Fatalf("receipts=%v, error=%v, downloads=%d, parent checks=%d, archives=%v", receipts, err, f.g.downloads, f.z.validations, f.z.archives)
	}
	files, err := os.ReadDir(f.o.Spool)
	if err != nil || len(files) != 0 {
		t.Fatalf("left data in spool: %v, %v", files, err)
	}
}

func TestEarlierPendingMatchNeedsQualifyingDeliveryForAutomaticRetry(t *testing.T) {
	f := newFixture(t)
	f.o.Direction = "receive"
	file := model.DriveFile{ID: "draft", Version: "1", Name: "draft - Kindle.pdf", CreatedTime: "2026-01-01T01:00:00Z"}
	f.g.files = []model.DriveFile{file}
	f.g.bytes[file.ID] = "%PDF-draft"
	if err := f.s.PutReceipt(model.Receipt{DriveFile: file, State: "pending", Parent: "draft"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutJob(model.Job{Paper: model.Paper{Key: "draft"}, State: "manual_upload", OutboundName: "draft.pdf", NamedAt: "2025-12-31T23:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.o.ReturnFile = file.ID
	f.run(t)
	if f.g.downloads != 0 || f.z.validations != 0 || len(f.z.archives) != 0 {
		t.Fatal("retried a pre-upgrade unqualified receipt")
	}
	f.o.MatchParent = "draft"
	f.run(t)
	if len(f.z.archives) != 1 || f.g.downloads != 1 {
		t.Fatal("explicit match failed to recover earlier intent")
	}
}

func TestRetryExcludesFilesCreatedAfterRejectedAttempt(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("paper", "%PDF-original")
	f.run(t)
	first := f.jobs(t)["paper"]
	if _, err := engine.Resolve(f.s, "paper", "rejected", first.GmailID); err != nil {
		t.Fatal(err)
	}
	f.o.Direction = "receive"
	f.g.files = []model.DriveFile{{ID: "unrelated", Version: "1", Name: first.OutboundName}}
	f.g.bytes["unrelated"] = "%PDF-unrelated"
	f.run(t) // Drive timestamps the unrelated file before the successful retry.
	f.o.Direction = "both"
	f.run(t)
	if len(f.g.sent) != 2 || f.g.downloads != 0 || len(f.z.archives) != 0 {
		t.Fatal("retry claimed an older same-name export", f.g.sent, f.z.archives)
	}
	f.g.files = append(f.g.files, model.DriveFile{ID: "returned", Version: "1", Name: first.OutboundName})
	f.g.bytes["returned"] = "%PDF-annotations"
	f.run(t)
	if len(f.z.archives) != 1 || f.z.archives[0].bytes != "%PDF-annotations" {
		t.Fatal(f.z.archives)
	}
}
