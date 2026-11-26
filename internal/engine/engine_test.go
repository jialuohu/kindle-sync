package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
func (g *fakeGoogle) Files(context.Context) ([]model.DriveFile, error) { return g.files, nil }
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
	f.g.files = []model.DriveFile{{ID: "a", Version: "1", Name: jobs["A"].Token + " Same paper.pdf"}, {ID: "b", Version: "1", Name: "Kindle_" + jobs["B"].Token + "_Same_paper.pdf"}}
	f.g.bytes["a"] = "%PDF-red mark"
	f.g.bytes["b"] = "%PDF-blue mark"
	f.run(t)
	f.run(t)
	if len(f.z.archives) != 2 || f.z.archives[0].parent != "A" || f.z.archives[0].bytes != "%PDF-red mark" || f.z.archives[1].parent != "B" {
		t.Fatalf("crossed parents/duplicates: %+v", f.z.archives)
	}
	f.g.files[0].Version = "2" // New revision, unchanged bytes.
	f.run(t)
	if len(f.z.archives) != 2 {
		t.Fatal("repeated bytes archived twice")
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
	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 1 || receipts[0].State != "needs_match" {
		t.Fatalf("receipts=%v err=%v", receipts, err)
	}
	f.g.files = nil // The original Drive revision is no longer available.
	f.add("unrelated", "%PDF-x")
	f.z.partial = true
	f.o.MatchFile, f.o.MatchParent = "missing", "CHOSEN"
	f.run(t)
	receipts, err = f.s.Receipts()
	if err != nil || receipts[0].Child != "child-key" || receipts[0].State != "pending" {
		t.Fatalf("partial progress lost: %+v, %v", receipts, err)
	}
	f.run(t)
	f.run(t)
	if f.z.validations != 1 || f.g.downloads != 1 || len(f.g.sent) != 0 || len(f.z.archives) != 2 || f.z.archives[1].previous != "child-key" || f.z.archives[1].bytes != "%PDF-handwritten" {
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

func TestResolveChecksStateAndPreservesToken(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("unknown", "%PDF-x")
	f.g.sendError = errors.New("lost response")
	f.run(t)
	token := f.jobs(t)["unknown"].Token
	if _, err := engine.Resolve(f.s, "unknown", "skip"); err == nil {
		t.Fatal("accepted invalid decision")
	}
	got, err := engine.Resolve(f.s, "unknown", "retry")
	if err != nil || got.Token != token || got.State != "queued" {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := engine.Resolve(f.s, "unknown", "received"); err == nil {
		t.Fatal("resolved unrelated job state")
	}
	f.run(t)
	confirmed, err := engine.Resolve(f.s, "unknown", "received")
	if err != nil || confirmed.State != "confirmed_received" {
		t.Fatal(confirmed, err)
	}
	count := len(f.g.sent)
	f.run(t)
	if len(f.g.sent) != count {
		t.Fatal("received paper replayed")
	}
}

func TestMatchOneRevisionAndRejectConflictingParent(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.g.bytes["file"] = "%PDF-first"
	f.g.files = []model.DriveFile{{ID: "file", Version: "1", Name: "Old Name.pdf"}}
	f.run(t)
	f.g.bytes["file"] = "%PDF-second"
	f.g.files[0].Version = "2"
	f.run(t)
	f.o.MatchFile, f.o.MatchParent = "file", "RIGHT"
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

func TestInvalidMatchLeavesReceiptAvailableForCorrection(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.g.files = []model.DriveFile{{ID: "held", Version: "1", Name: "unrecognized.pdf"}}
	f.g.bytes["held"] = "%PDF-annotations"
	f.run(t)
	f.o.MatchFile, f.o.MatchParent = "held", "INVALID"
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("accepted nonexistent parent")
	}
	receipts, err := f.s.Receipts()
	if err != nil || len(receipts) != 1 || receipts[0].Parent != "" || receipts[0].State != "needs_match" || len(f.z.archives) > 0 {
		t.Fatalf("invalid target altered held receipt: %+v, %v", receipts, err)
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
	name := f.jobs(t)["SOURCE"].Token + " annotated.pdf"
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
