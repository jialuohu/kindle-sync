package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kindle-sync/internal/engine"
	"kindle-sync/internal/model"
	"kindle-sync/internal/state"
)

type fakeLibrary struct {
	validations int
	collections map[string][]model.Paper
	sources     map[string][]model.Attachment
	pdfs        map[string]string
	archives    []savedPDF
	partial     bool
}
type savedPDF struct{ parent, name, bytes, previous string }

func (z *fakeLibrary) ValidateParent(_ context.Context, key string) error {
	z.validations++
	if key == "INVALID" {
		return errors.New("not an existing regular Zotero item")
	}
	return nil
}
func (z *fakeLibrary) Members(_ context.Context, k string) ([]model.Paper, error) {
	return z.collections[k], nil
}
func (z *fakeLibrary) Sources(_ context.Context, k string) ([]model.Attachment, error) {
	return z.sources[k], nil
}
func (z *fakeLibrary) Download(_ context.Context, a model.Attachment, p string) error {
	if data, ok := z.pdfs[a.Key]; ok {
		return os.WriteFile(p, []byte(data), 0600)
	}
	return errors.New("not synced yet")
}
func (z *fakeLibrary) Archive(_ context.Context, parent, p, key string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	z.archives = append(z.archives, savedPDF{parent, filepath.Base(p), string(b), key})
	if z.partial {
		z.partial = false
		return "child-key", errors.New("upload interrupted")
	}
	return "child-key", nil
}

type fakeGoogle struct {
	sent      []string
	sendError error
	files     []model.DriveFile
	bytes     map[string]string
	downloads int
	fileLists int
}

func (g *fakeGoogle) Send(_ context.Context, p, name string) (string, error) {
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	g.sent = append(g.sent, name)
	if g.sendError != nil {
		return "", g.sendError
	}
	return "gmail-id", nil
}
func (g *fakeGoogle) Files(context.Context) ([]model.DriveFile, error) {
	g.fileLists++
	for i := range g.files {
		if g.files[i].CreatedTime == "" {
			g.files[i].CreatedTime = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	return g.files, nil
}
func (g *fakeGoogle) Download(_ context.Context, f model.DriveFile, p string) error {
	g.downloads++
	return os.WriteFile(p, []byte(g.bytes[f.ID]), 0600)
}

type fixture struct {
	z *fakeLibrary
	g *fakeGoogle
	s *state.Store
	o model.Options
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := state.Open(t.TempDir(), "test", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &fixture{&fakeLibrary{collections: map[string][]model.Paper{}, sources: map[string][]model.Attachment{}, pdfs: map[string]string{}}, &fakeGoogle{bytes: map[string]string{}}, s, model.Options{Root: "root", Spool: t.TempDir(), MaxEmailBytes: 20}}
}
func (f *fixture) run(t *testing.T) model.Report {
	t.Helper()
	res, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func (f *fixture) add(key string, pdf string) {
	f.z.collections["root"] = append(f.z.collections["root"], model.Paper{Key: key, Title: "Same paper title"})
	f.z.sources[key] = []model.Attachment{{Key: "source-" + key}}
	if pdf != "" {
		f.z.pdfs["source-"+key] = pdf
	}
}
func (f *fixture) jobs(t *testing.T) map[string]model.Job {
	t.Helper()
	all, err := f.s.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Job{}
	for _, j := range all {
		out[j.Key] = j
	}
	return out
}

func TestBaselineBatchOversizeAndDelayedSource(t *testing.T) {
	f := newFixture(t)
	f.add("old", "%PDF-old")
	f.run(t)
	if len(f.g.sent) != 0 || f.jobs(t)["old"].State != "baseline" {
		t.Fatal("initial backlog sent")
	}
	f.add("one", "%PDF-small12345")
	f.add("two", "%PDF-small67890")
	f.add("big", "%PDF-"+strings.Repeat("x", 30))
	f.add("late", "")
	f.z.collections["root"] = append(f.z.collections["root"], f.z.collections["root"][1])
	f.o.DryRun = true
	f.run(t)
	if len(f.jobs(t)) != 1 || len(f.g.sent) != 0 {
		t.Fatal("preview mutated queue")
	}
	f.o.DryRun = false
	f.run(t)
	jobs := f.jobs(t)
	if len(f.g.sent) != 2 || f.g.sent[0] == f.g.sent[1] || jobs["late"].State != "waiting_pdf" || jobs["big"].State != "manual_upload" {
		t.Fatalf("jobs=%+v, mail=%v", jobs, f.g.sent)
	}
	info, err := os.Stat(jobs["big"].ManualFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("missing private staged PDF: %v", err)
	}
	f.z.pdfs["source-late"] = "%PDF-now available"
	f.z.collections["subset"] = []model.Paper{{Key: "old"}, {Key: "one"}}
	f.o.Backfill = "subset"
	f.run(t)
	f.run(t)
	if len(f.g.sent) != 3 || f.jobs(t)["late"].State != "waiting_pdf" {
		t.Fatal("backfill processed queued work outside the selected collection")
	}
	f.o.Backfill = ""
	f.run(t)
	if len(f.g.sent) != 4 {
		t.Fatalf("sent %d; expected one e-mail per eligible paper", len(f.g.sent))
	}
}

func TestUncertainSendsAreNotReplayed(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("unknown", "%PDF-x")
	f.g.sendError = errors.New("connection lost")
	f.run(t)
	f.run(t)
	if f.jobs(t)["unknown"].State != "uncertain" || len(f.g.sent) != 1 {
		t.Fatal("ambiguous send was retried")
	}
	if err := f.s.PutJob(model.Job{Paper: model.Paper{Key: "crashed"}, State: "sending"}); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if f.jobs(t)["crashed"].State != "uncertain" || len(f.g.sent) != 1 {
		t.Fatal("interrupted send retried")
	}
	f.add("rejected", "%PDF-y")
	f.g.sendError = model.ErrSendRejected
	f.run(t)
	f.g.sendError = nil
	f.run(t)
	if f.jobs(t)["rejected"].State != "submitted" || len(f.g.sent) != 3 {
		t.Fatal("definite rejection not recoverable")
	}
}

func TestSourceChoiceAndRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("A", "%PDF-A")
	f.add("B", "%PDF-B")
	f.z.sources["A"] = append(f.z.sources["A"], model.Attachment{Key: "supplement"})
	f.run(t)
	if f.jobs(t)["A"].State != "needs_source" {
		t.Fatal("ambiguous source sent")
	}
	f.o.Item, f.o.Attachment = "A", "source-A"
	f.run(t)
	f.o.Item, f.o.Attachment = "", ""
	jobs := f.jobs(t)
	f.g.files = []model.DriveFile{{ID: "a", Version: "1", Name: jobs["A"].OutboundName}, {ID: "b", Version: "1", Name: strings.TrimSuffix(jobs["B"].OutboundName, ".pdf") + " - Kindle.pdf"}}
	f.g.bytes["a"] = "%PDF-red mark"
	f.g.bytes["b"] = "%PDF-blue mark"
	f.run(t)
	f.run(t)
	if len(f.z.archives) != 2 || f.z.archives[0].parent != "A" || f.z.archives[0].bytes != "%PDF-red mark" || f.z.archives[1].parent != "B" {
		t.Fatalf("crossed parents/duplicates: %+v", f.z.archives)
	}
	f.g.files[0].Version = "2" // New revision, unchanged bytes.
	duplicate := f.run(t)
	if len(f.z.archives) != 2 || len(duplicate.Actions) != 1 || duplicate.Actions[0] != "already archived: a" {
		t.Fatalf("repeated bytes wrote or reported another archive: %+v", duplicate)
	}
	f.g.files[0].Version = "3"
	f.g.bytes["a"] = "%PDF-more ink"
	f.run(t)
	if len(f.z.archives) != 3 || f.z.archives[2].name == f.z.archives[0].name {
		t.Fatal("new export did not get a new attachment")
	}
}

func TestManualMatchAndPartialArchiveFromCachedBytes(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.g.bytes["missing"] = "%PDF-handwritten"
	f.g.files = []model.DriveFile{{ID: "missing", Version: "2", Name: "lost the title.pdf"}}
	f.run(t)
	f.add("unrelated", "%PDF-x")
	f.z.partial = true
	f.o.ReturnFile, f.o.MatchParent = "missing", "CHOSEN"
	f.run(t)
	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 1 || receipts[0].Child != "child-key" || receipts[0].State != "pending" {
		t.Fatalf("partial progress lost: %+v, %v", receipts, err)
	}
	f.g.files = nil // Resume the explicit import after Drive stops serving the revision.
	f.run(t)
	f.run(t)
	if f.g.downloads != 1 || len(f.g.sent) != 0 || len(f.z.archives) != 2 || f.z.archives[1].previous != "child-key" || f.z.archives[1].bytes != "%PDF-handwritten" {
		t.Fatalf("mail=%v downloads=%d archives=%+v", f.g.sent, f.g.downloads, f.z.archives)
	}
}

func TestDryRunDoesNotInitialize(t *testing.T) {
	f := newFixture(t)
	f.o.DryRun = true
	f.add("old", "%PDF-x")
	f.run(t)
	did, err := f.s.Initialized()
	if err != nil || did || len(f.jobs(t)) != 0 {
		t.Fatalf("preview initialized database %v %v", did, err)
	}
}

func TestScopedBackfill(t *testing.T) {
	f := newFixture(t)
	f.add("inside", "%PDF-real")
	f.run(t)
	f.z.collections["outside"] = []model.Paper{{Key: "unrelated"}, {Key: "inside"}}
	f.o.Backfill = "outside"
	f.run(t)
	if len(f.g.sent) != 1 || len(f.jobs(t)) != 1 {
		t.Fatalf("backfill escaped configured root: %+v", f.jobs(t))
	}
}

func TestResolveChecksStateAndPreservesFilename(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("unknown", "%PDF-x")
	f.g.sendError = errors.New("lost response")
	f.run(t)
	name := f.jobs(t)["unknown"].OutboundName
	if _, err := engine.Resolve(f.s, "unknown", "skip", ""); err == nil {
		t.Fatal("accepted invalid decision")
	}
	got, err := engine.Resolve(f.s, "unknown", "retry", "")
	if err != nil || name == "" || got.OutboundName != name || got.State != "queued" {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := engine.Resolve(f.s, "unknown", "received", ""); err == nil {
		t.Fatal("resolved unrelated job state")
	}
	f.run(t)
	confirmed, err := engine.Resolve(f.s, "unknown", "received", "")
	if err != nil || confirmed.State != "confirmed_received" {
		t.Fatal(confirmed, err)
	}
	count := len(f.g.sent)
	f.run(t)
	if len(f.g.sent) != count {
		t.Fatal("received paper replayed")
	}
}

func TestArtifactOverrideSurvivesMissingSourceAndRetry(t *testing.T) {
	f := newFixture(t)
	f.add("DEMO1234", "")
	f.run(t) // Simulate a baseline saved before creator/date metadata was available.
	f.z.collections["root"][0].FirstAuthor = "Reed"
	f.z.collections["root"][0].Year = "2026"
	f.o.Item, f.o.Artifact = "DEMO1234", "Quill"
	f.o.ArtifactNames = map[string]string{"DEMO1234": "less-specific"}
	f.o.DryRun = true
	preview := f.run(t)
	if !strings.Contains(strings.Join(preview.Actions, "\n"), "reed26-quill.pdf") || f.jobs(t)["DEMO1234"].OutboundName != "" {
		t.Fatal(preview, f.jobs(t))
	}
	f.o.DryRun = false
	f.run(t)
	j := f.jobs(t)["DEMO1234"]
	if j.OutboundName != "reed26-quill.pdf" || j.State != "waiting_pdf" || j.Token != "" {
		t.Fatal(j)
	}
	f.o.Item, f.o.Artifact = "", ""
	f.z.pdfs["source-DEMO1234"] = "%PDF-new"
	f.g.sendError = model.ErrSendRejected
	f.run(t)
	f.g.sendError = nil
	f.z.collections["root"][0].Title = "changed in Zotero"
	f.run(t)
	if len(f.g.sent) != 2 || f.g.sent[0] != "reed26-quill.pdf" || f.g.sent[1] != f.g.sent[0] {
		t.Fatal(f.g.sent)
	}
}

func TestConfiguredArtifactAndOlderDriveExports(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("DEMO1234", "%PDF-x")
	p := &f.z.collections["root"][0]
	p.FirstAuthor, p.Year, p.Title = "Reed", "2026", "Indexing library records"
	f.o.ArtifactNames = map[string]string{"DEMO1234": "Quill"}
	f.g.files = []model.DriveFile{{ID: "older", Version: "1", Name: "reed26-quill - Kindle.pdf", CreatedTime: "2001-12-31T23:59:00.120-05:00"}}
	f.g.bytes["older"] = "%PDF-earlier"
	f.run(t)
	if len(f.g.sent) != 1 || f.g.sent[0] != "reed26-quill.pdf" || len(f.z.archives) != 0 {
		t.Fatal(f.g.sent, f.z.archives)
	}
	// An explicitly selected older file can still be archived.
	f.o.ReturnFile, f.o.MatchParent = "older", "DEMO1234"
	f.run(t)
	if len(f.z.archives) != 1 || f.z.archives[0].parent != "DEMO1234" {
		t.Fatal(f.z.archives)
	}
}

func TestCollidingNamesReturnToTheirOwnPaper(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	for _, k := range []string{"AAAA1111", "BBBB2222"} {
		f.add(k, "%PDF-short")
	}
	for i := range f.z.collections["root"] {
		p := &f.z.collections["root"][i]
		p.FirstAuthor, p.Year, p.Title = "Reed", "2026", "Quill: A system"
	}
	f.z.pdfs["source-BBBB2222"] = "%PDF-" + strings.Repeat("b", 30)
	f.run(t)
	j := f.jobs(t)
	if len(f.g.sent) != 1 || f.g.sent[0] != "reed26-quill.pdf" || j["BBBB2222"].OutboundName != "reed26-quill-bbbb2222.pdf" || filepath.Base(j["BBBB2222"].ManualFile) != "reed26-quill-bbbb2222.pdf" {
		t.Fatal(f.g.sent, j)
	}
	for id, name := range map[string]string{"A": "Reed26-Quill - Kindle.PDF", "B": "reed26-quill-bbbb2222.pdf", "other": "reed26-quill-extra.pdf"} {
		f.g.files = append(f.g.files, model.DriveFile{ID: id, Version: "1", Name: name})
		f.g.bytes[id] = "%PDF-" + id
	}
	f.run(t)
	if len(f.z.archives) != 1 || f.z.archives[0].parent != "AAAA1111" || f.g.downloads != 1 {
		t.Fatal("only the submitted paper should be claimed automatically", f.z.archives)
	}
	f.o.ReturnFile, f.o.MatchParent = "B", "BBBB2222"
	f.run(t)
	if len(f.z.archives) != 2 || f.z.archives[0].parent != "AAAA1111" || f.z.archives[1].parent != "BBBB2222" {
		t.Fatal(f.z.archives)
	}
}

func TestLegacyRetryAndAmbiguousExport(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	const token = "KRS-ABCDEFGHIJKLMNOP"
	f.add("old", "%PDF-original")
	if err := f.s.PutJob(model.Job{Paper: model.Paper{Key: "old", Title: "Original Title"}, State: "queued", Token: token, Source: "source-old", SourceHash: "old-hash"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ambiguous1", "ambiguous2"} {
		if err := f.s.PutJob(model.Job{Paper: model.Paper{Key: k}, State: "submitted", OutboundName: "reed26-quill.pdf"}); err != nil {
			t.Fatal(err)
		}
	}
	f.run(t)
	if len(f.g.sent) != 1 || f.g.sent[0] != token+" Original Title.pdf" {
		t.Fatal(f.g.sent)
	}
	f.g.files = []model.DriveFile{{ID: "legacy", Version: "1", Name: "Kindle_" + token + "_Original_Title.pdf"}, {ID: "duplicate", Version: "1", Name: "reed26-quill - Kindle.pdf"}}
	f.g.bytes["legacy"], f.g.bytes["duplicate"] = "%PDF-legacy", "%PDF-duplicate"
	f.run(t)
	if len(f.z.archives) != 1 || f.z.archives[0].parent != "old" {
		t.Fatal(f.z.archives)
	}
}

func TestPreviouslyUnmatchedExportDoesNotAcquireNewParent(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.g.files = []model.DriveFile{{ID: "earlier", Version: "1", Name: "reed26-quill - Kindle.pdf"}}
	f.g.bytes["earlier"] = "%PDF-earlier"
	f.run(t)
	f.add("PAPER123", "%PDF-original")
	p := &f.z.collections["root"][0]
	p.FirstAuthor, p.Year, p.Title = "Reed", "2026", "Quill: Old names"
	f.run(t)
	if len(f.g.sent) != 1 || f.g.sent[0] != "reed26-quill.pdf" || len(f.z.archives) != 0 || f.g.downloads != 0 {
		t.Fatal(f.g.sent, f.z.archives)
	}
}

func TestMatchOneRevisionAndRejectConflictingParent(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	// Prior releases kept unassigned exports. The cached revision remains
	// available for an explicit match after Drive replaces it.
	old := model.DriveFile{ID: "file", Version: "1", Name: "Old Name.pdf"}
	seedLegacyHeld(t, f, old, "%PDF-first")
	f.g.bytes["file"] = "%PDF-second"
	f.g.files = []model.DriveFile{{ID: "file", Version: "2", Name: old.Name}}
	f.run(t)
	f.o.ReturnFile, f.o.MatchParent = "file", "RIGHT"
	f.run(t)
	if len(f.z.archives) != 1 || f.z.archives[0].bytes != "%PDF-second" {
		t.Fatal(f.z.archives)
	}
	f.o.MatchParent = "WRONG"
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("reassigned existing parent")
	}
	f.g.files = nil
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("implicitly selected one of several cached revisions")
	}
	f.o.MatchVersion = "1"
	f.run(t)
	if len(f.z.archives) != 2 || f.z.archives[1].bytes != "%PDF-first" || f.z.archives[1].parent != "WRONG" {
		t.Fatal(f.z.archives)
	}
}

func TestReceiveOneCurrentExportAndResume(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("pilot", "%PDF-pilot")
	f.run(t)
	f.add("unsent", "%PDF-queued")
	title := f.jobs(t)["pilot"].OutboundName
	f.g.files = []model.DriveFile{{ID: "other", Name: title, Version: "1"}, {ID: "pilot-export", Name: title, Version: "2"}}
	f.g.bytes["pilot-export"] = "%PDF-annotated"
	f.o.ReturnFile = "pilot-export"
	f.z.partial = true
	if report := f.run(t); len(report.Problems) != 1 {
		t.Fatal(report)
	}
	f.run(t)
	f.run(t)
	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 1 || receipts[0].ID != "pilot-export" || receipts[0].Parent != "pilot" || receipts[0].State != "archived" {
		t.Fatal(receipts, err)
	}
	if len(f.jobs(t)) != 1 || len(f.g.sent) != 1 || f.g.downloads != 1 || len(f.z.archives) != 2 || f.z.archives[1].previous != "child-key" {
		t.Fatalf("jobs=%v mail=%v downloads=%d archive=%v", f.jobs(t), f.g.sent, f.g.downloads, f.z.archives)
	}
	f.g.files = nil
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("selected a stale revision after the file left the bound folder")
	}
}

func TestPilotCanSendBeforeFirstDriveExport(t *testing.T) {
	f := newFixture(t)
	f.o.SkipReturns = true
	f.run(t)
	f.add("pilot", "%PDF-pilot")
	f.g.files = []model.DriveFile{{ID: "invalid-without-version"}}
	f.run(t)
	if len(f.g.sent) != 1 || f.g.downloads != 0 {
		t.Fatalf("mail=%v downloads=%d", f.g.sent, f.g.downloads)
	}
}

type faultLibrary struct {
	*fakeLibrary
	rootError  error
	fetchError error
}

func (l faultLibrary) Members(ctx context.Context, key string) ([]model.Paper, error) {
	if key == "root" && l.rootError != nil {
		return nil, l.rootError
	}
	return l.fakeLibrary.Members(ctx, key)
}
func (l faultLibrary) Download(ctx context.Context, a model.Attachment, p string) error {
	if l.fetchError != nil {
		return l.fetchError
	}
	return l.fakeLibrary.Download(ctx, a, p)
}
func TestBackfillRequiresReadingQueueAndOversizedDownloadIsTerminal(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("large", "%PDF-test")
	f.z.collections["scope"] = f.z.collections["root"]
	f.o.Backfill = "scope"
	l := faultLibrary{fakeLibrary: f.z, rootError: errors.New("failed to load scope")}
	if _, err := engine.Run(context.Background(), f.s, l, f.g, f.o); err == nil || len(f.g.sent) > 0 {
		t.Fatalf("unsafe backfill: %v", err)
	}
	f.o.Backfill = ""
	l.rootError = nil
	l.fetchError = model.ErrTooLarge
	_, err := engine.Run(context.Background(), f.s, l, f.g, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if f.jobs(t)["large"].State != "too_large" {
		t.Fatal("source size error was classified as retryable")
	}
}

type failSubmitted struct{ model.Store }

func (s failSubmitted) PutJob(j model.Job) error {
	if j.State == "submitted" {
		return errors.New("disk full after Gmail acceptance")
	}
	return s.Store.PutJob(j)
}
func TestLostLocalCommitAfterGmailAccepted(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("one", "%PDF-tested")
	_, err := engine.Run(context.Background(), failSubmitted{f.s}, f.z, f.g, f.o)
	if err == nil || len(f.g.sent) != 1 || f.jobs(t)["one"].State != "sending" {
		t.Fatal("missing durable submission intent")
	}
	f.run(t)
	if len(f.g.sent) != 1 || f.jobs(t)["one"].State != "uncertain" {
		t.Fatal("replayed accepted message after crash")
	}
}

func TestInvalidMatchLeavesPDFOnDriveForCorrection(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.g.files = []model.DriveFile{{ID: "held", Version: "1", Name: "unrecognized.pdf"}}
	f.g.bytes["held"] = "%PDF-annotations"
	f.run(t)
	f.o.ReturnFile, f.o.MatchParent = "held", "INVALID"
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("accepted nonexistent parent")
	}
	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 0 || f.g.downloads != 0 || len(f.z.archives) > 0 {
		t.Fatalf("invalid target imported the export: %+v, %v", receipts, err)
	}
	f.o.MatchParent = "VALID"
	f.run(t)
	if len(f.z.archives) != 1 || f.z.archives[0].parent != "VALID" {
		t.Fatal(f.z.archives)
	}
}

func TestSupersededUndownloadedRevisionStopsRetrying(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("SOURCE", "%PDF-original")
	f.run(t)
	name := f.jobs(t)["SOURCE"].OutboundName
	f.g.files = []model.DriveFile{{ID: "replaced", Version: "1", Name: name}}
	f.g.bytes["replaced"] = "incomplete non-PDF"
	if res := f.run(t); len(res.Problems) != 1 {
		t.Fatalf("expected retryable download failure: %+v", res)
	}
	f.g.files[0].Version = "2"
	f.g.bytes["replaced"] = "%PDF-updated"
	if res := f.run(t); len(res.Problems) != 0 {
		t.Fatalf("superseded version caused an error: %+v", res)
	}
	if res := f.run(t); len(res.Actions) != 0 || len(res.Problems) != 0 {
		t.Fatalf("replayed superseded revision: %+v", res)
	}
	all, err := f.s.Receipts()
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	states := map[string]string{}
	for _, x := range all {
		states[x.Version] = x.State
	}
	if states["1"] != "superseded" || states["2"] != "archived" || len(f.z.archives) != 1 {
		t.Fatal(states, f.z.archives)
	}
}

func TestScopedSendLeavesOtherJobsAndExportsForLater(t *testing.T) {
	f := newFixture(t)
	f.o.Direction = "send"
	f.add("baseline", "%PDF-old")
	f.add("selected", "%PDF-one")
	f.o.Item = "selected"
	f.run(t)
	f.run(t)
	if len(f.g.sent) != 1 || f.g.fileLists != 0 || f.jobs(t)["baseline"].State != "baseline" {
		t.Fatal("initial selected send failed to seed the collection or repeated a delivery")
	}
	f.add("new", "%PDF-two")
	f.add("later", "%PDF-three")
	if err := f.s.PutJob(model.Job{Paper: model.Paper{Key: "interrupted"}, State: "sending"}); err != nil {
		t.Fatal(err)
	}
	jobs := f.jobs(t)
	f.g.files = []model.DriveFile{{ID: "export", Version: "1", Name: jobs["selected"].OutboundName}}
	f.g.bytes["export"] = "%PDF-notes"
	f.o.Item = "new"
	f.run(t)
	if len(f.g.sent) != 2 || f.g.fileLists != 0 || len(f.z.archives) != 0 || f.jobs(t)["interrupted"].State != "sending" {
		t.Fatal("single-paper send touched an unrelated job or an export")
	}
	if _, ok := f.jobs(t)["later"]; ok {
		t.Fatal("targeted discovery changed another job")
	}
	f.o.Item, f.o.Direction = "", "receive"
	f.run(t)
	f.run(t)
	if len(f.g.sent) != 2 || len(f.z.archives) != 1 || f.z.archives[0].parent != "selected" || f.jobs(t)["interrupted"].State != "sending" {
		t.Fatal("receive-only sync changed outgoing state or duplicated an archive")
	}
	if _, ok := f.jobs(t)["later"]; ok {
		t.Fatal("receive-only sync discovered unrelated outgoing jobs")
	}
}

func TestTargetedSendRequiresCurrentMembership(t *testing.T) {
	f := newFixture(t)
	f.add("moved", "%PDF-test")
	f.run(t)
	f.o.Item = "moved"
	f.z.collections["root"] = nil
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("sent an item from cached membership after removal from the collection")
	}
	l := faultLibrary{fakeLibrary: f.z, rootError: errors.New("membership unavailable")}
	if _, err := engine.Run(context.Background(), f.s, l, f.g, f.o); err == nil {
		t.Fatal("used stale membership when Zotero lookup failed")
	}
	if len(f.g.sent) != 0 || f.jobs(t)["moved"].State != "baseline" {
		t.Fatal("failed lookup changed the selected item")
	}
}

func TestReceiveOnlyLeavesInitialBaselineUnseeded(t *testing.T) {
	f := newFixture(t)
	f.add("existing", "%PDF-original")
	f.o.Direction = "receive"
	f.run(t)
	init, err := f.s.Initialized()
	if err != nil || init || len(f.jobs(t)) != 0 || f.g.fileLists != 1 {
		t.Fatal("receive-only sync initiated outbound discovery", err)
	}
}

func TestPendingSendWaitsForVerifiedMembership(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("delayed", "")
	f.run(t)
	if f.jobs(t)["delayed"].State != "waiting_pdf" {
		t.Fatal("expected a queued retry")
	}
	f.z.pdfs["source-delayed"] = "%PDF-ready"
	f.o.Direction = "send"
	unavailable := faultLibrary{fakeLibrary: f.z, rootError: errors.New("library unavailable")}
	report, err := engine.Run(context.Background(), f.s, unavailable, f.g, f.o)
	if err != nil || len(report.Problems) == 0 || len(f.g.sent) != 0 {
		t.Fatal("failed membership lookup sent queued paper or went unreported", report, err)
	}
	f.o.Direction = "both"
	beforeLists := f.g.fileLists
	_, err = engine.Run(context.Background(), f.s, unavailable, f.g, f.o)
	if err != nil || len(f.g.sent) != 0 || f.g.fileLists != beforeLists+1 {
		t.Fatal("library outage should leave independent Drive intake available", err)
	}
	f.z.collections["root"] = nil
	f.run(t)
	if f.jobs(t)["delayed"].State != "waiting_pdf" || len(f.g.sent) != 0 {
		t.Fatal("the retry was sent after the paper left the watched collection")
	}
	f.add("delayed", "%PDF-ready")
	f.run(t)
	f.run(t)
	if len(f.g.sent) != 1 || f.jobs(t)["delayed"].State != "submitted" {
		t.Fatal("paper did not resume exactly once after re-entering the collection")
	}
}
