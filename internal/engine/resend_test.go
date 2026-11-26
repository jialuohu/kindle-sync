package engine_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"kindle-sync/internal/engine"
	"kindle-sync/internal/model"
)

func renamedPaper(t *testing.T) (*fixture, model.Job) {
	t.Helper()
	f := newFixture(t)
	f.run(t)
	f.add("PAPER001", "%PDF-example")
	p := &f.z.collections["root"][0]
	p.Title, p.FirstAuthor = "Quill: an algorithm", "Reed"
	f.run(t)
	old := f.jobs(t)[p.Key]
	if old.OutboundName != "reednd-quill.pdf" || old.State != "submitted" {
		t.Fatal(old)
	}
	f.g.sent = nil
	p.AddedYear, f.z.pdfYear = "2024", "2026"
	f.o.Item, f.o.ExpectedResendName = p.Key, "reed26-quill.pdf"
	return f, old
}

func TestResendRequiresExpectedFilenameAndKeepsPreviousDelivery(t *testing.T) {
	f, old := renamedPaper(t)
	f.add("PAPER002", "%PDF-other")
	f.o.DryRun = true
	preview := strings.Join(f.run(t).Actions, "\n")
	if !strings.Contains(preview, "reed26-quill.pdf") || !strings.Contains(preview, "reed24-quill.pdf") {
		t.Fatal(preview)
	}
	if !reflect.DeepEqual(f.jobs(t)[old.Key], old) || len(f.g.sent) != 0 {
		t.Fatal("preview changed a delivery")
	}
	f.o.DryRun = false
	f.run(t)
	newJob := f.jobs(t)[old.Key]
	if newJob.OutboundName != f.o.ExpectedResendName || newJob.State != "submitted" || newJob.NamedAt == old.NamedAt || newJob.Year != "2026" || !reflect.DeepEqual(newJob.PreviousDeliveries, []model.Delivery{old.CurrentDelivery()}) {
		t.Fatal(newJob)
	}
	f.run(t)
	f.o.ExpectedResendName = old.OutboundName
	f.run(t)
	if len(f.g.sent) != 1 || f.g.sent[0] != "reed26-quill.pdf" || len(f.jobs(t)) != 1 || !reflect.DeepEqual(f.jobs(t)[old.Key], newJob) {
		t.Fatalf("mail %v jobs %+v", f.g.sent, f.jobs(t))
	}
}

func TestResendPreflightFailurePreservesSubmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*fixture)
	}{
		{"filename mismatch", func(f *fixture) { f.o.ExpectedResendName = "wrong.pdf" }},
		{"missing source", func(f *fixture) { delete(f.z.sources, f.o.Item) }},
		{"changed bytes", func(f *fixture) { f.z.pdfs["source-"+f.o.Item] = "%PDF-updated" }},
		{"download failure", func(f *fixture) { delete(f.z.pdfs, "source-"+f.o.Item) }},
		{"too large", func(f *fixture) { f.o.MaxEmailBytes = 1 }},
		{"missing member", func(f *fixture) { f.z.collections["root"] = nil }},
		{"no submission", func(f *fixture) { f.add("PAPER002", "%PDF-unsent"); f.o.Item = "PAPER002" }},
		{"unscoped", func(f *fixture) { f.o.Item = "" }},
		{"both directions", func(f *fixture) { f.o.Direction = "both" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, old := renamedPaper(t)
			tc.edit(f)
			if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
				t.Fatal("expected error")
			}
			if len(f.g.sent) != 0 || len(f.jobs(t)) != 1 || !reflect.DeepEqual(f.jobs(t)[old.Key], old) {
				t.Fatal(f.jobs(t), f.g.sent)
			}
		})
	}
}

func TestResendUncertainRequiresDeviceCheck(t *testing.T) {
	f, old := renamedPaper(t)
	f.g.sendError = errors.New("connection lost")
	if got := f.run(t); len(got.Problems) != 1 {
		t.Fatal(got)
	}
	attempt := f.jobs(t)[old.Key]
	if attempt.State != "uncertain" || attempt.GmailID != "" || !reflect.DeepEqual(attempt.PreviousDeliveries, []model.Delivery{old.CurrentDelivery()}) {
		t.Fatal(attempt)
	}
	if _, err := engine.Run(context.Background(), f.s, f.z, f.g, f.o); err == nil {
		t.Fatal("uncertain reissue allowed")
	}
	f.o.ExpectedResendName, f.o.Item = "", ""
	f.run(t)
	if len(f.g.sent) != 1 || !reflect.DeepEqual(f.jobs(t)[old.Key], attempt) {
		t.Fatal(f.g.sent, f.jobs(t))
	}
}

func TestRejectedResendRestoresPreviousJob(t *testing.T) {
	f, old := renamedPaper(t)
	f.g.sendError = model.ErrSendRejected
	if report := f.run(t); len(report.Problems) != 1 || !reflect.DeepEqual(f.jobs(t)[old.Key], old) {
		t.Fatal(report, f.jobs(t))
	}
	f.g.sendError = nil
	f.o.Item, f.o.ExpectedResendName = "", ""
	f.run(t)
	if len(f.g.sent) != 1 {
		t.Fatal("scheduler retried the rejected reissue")
	}
}

type rejectSubmissionWrite struct{ model.Store }

func (s rejectSubmissionWrite) PutJob(j model.Job) error {
	if j.State == "submitted" {
		return errors.New("cannot record Gmail response")
	}
	return s.Store.PutJob(j)
}

func TestResendCrashAfterGmailPreservesBothIdentities(t *testing.T) {
	f, old := renamedPaper(t)
	if _, err := engine.Run(context.Background(), rejectSubmissionWrite{f.s}, f.z, f.g, f.o); err == nil {
		t.Fatal("expected a final store write failure")
	}
	j := f.jobs(t)[old.Key]
	if j.State != "sending" || j.OutboundName != f.o.ExpectedResendName || !reflect.DeepEqual(j.PreviousDeliveries, []model.Delivery{old.CurrentDelivery()}) {
		t.Fatal(j)
	}
	f.o.Item, f.o.ExpectedResendName = "", ""
	f.run(t)
	f.run(t)
	if len(f.g.sent) != 1 || f.jobs(t)[old.Key].State != "uncertain" {
		t.Fatal(f.jobs(t), f.g.sent)
	}
}
