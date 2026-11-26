package engine_test

import (
	"strings"
	"testing"

	"kindle-sync/internal/model"
)

func TestYearForFirstDelivery(t *testing.T) {
	for _, tc := range []struct {
		label, zotero, doi, registry, page, added, preview, sent, recorded string
		lookups                                                            int
		provisional                                                        bool
	}{
		{"Zotero", "2001", "10.1234/sample", "2002", "2003", "2004", "01", "01", "2001", 0, false},
		{"DOI", "", "10.1234/sample", "2002", "2003", "2004", "02", "02", "2002", 2, false},
		{"PDF after registry miss", "", "10.1234/sample", "", "2003", "2004", "04", "03", "2003", 2, true},
		{"added year", "", "", "", "", "2004", "04", "04", "", 0, true},
		{"undated", "", "", "", "", "", "nd", "nd", "", 0, true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			f := newFixture(t)
			f.run(t)
			f.add("PAPER001", "%PDF-example")
			p := &f.z.collections["root"][0]
			p.FirstAuthor, p.Title = "Reed", "Quill: a system"
			p.Year, p.DOI, p.AddedYear = tc.zotero, tc.doi, tc.added
			f.z.doiYear, f.z.pdfYear = tc.registry, tc.page
			f.o.DryRun = true
			preview := strings.Join(f.run(t).Actions, "\n")
			if !strings.Contains(preview, "reed"+tc.preview+"-quill.pdf") || strings.Contains(preview, "provisional") != tc.provisional {
				t.Fatal(preview)
			}
			f.o.DryRun = false
			f.run(t)
			f.run(t) // A completed delivery must not look up its year again.
			if len(f.g.sent) != 1 || f.g.sent[0] != "reed"+tc.sent+"-quill.pdf" || f.jobs(t)[p.Key].Year != tc.recorded {
				t.Fatal(f.g.sent, f.jobs(t)[p.Key])
			}
			if len(f.z.doiLookups) != tc.lookups {
				t.Fatal(f.z.doiLookups)
			}
		})
	}
}

func TestDelayedPDFRefinesOverrideBeforeFirstAttemptOnly(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("PAPER001", "")
	p := &f.z.collections["root"][0]
	p.FirstAuthor, p.AddedYear = "Reed", "2026"
	f.o.Item, f.o.Artifact = p.Key, "Quill"
	f.run(t)
	if j := f.jobs(t)[p.Key]; j.OutboundName != "reed26-quill.pdf" || j.State != "waiting_pdf" {
		t.Fatal(j)
	}
	f.o.Item, f.o.Artifact = "", ""
	f.z.pdfs["source-"+p.Key], f.z.pdfYear = "%PDF-example", "2024"
	f.g.sendError = model.ErrSendRejected
	f.run(t)
	p.Year, p.DOI, f.z.pdfYear = "2020", "10.1234/new", "2018"
	f.g.sendError = nil
	f.run(t)
	if len(f.g.sent) != 2 || f.g.sent[0] != "reed24-quill.pdf" || f.g.sent[1] != f.g.sent[0] || len(f.z.doiLookups) != 0 {
		t.Fatal(f.g.sent, f.z.doiLookups)
	}
}

func TestPreUpgradeUndatedDeliveryStillReceivesNotes(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	f.add("PAPER001", "%PDF-example")
	p := &f.z.collections["root"][0]
	p.Title, p.FirstAuthor, p.AddedYear = "Quill: a system", "Reed", "2026"
	const oldName = "reednd-quill.pdf"
	if err := f.s.PutJob(model.Job{
		Paper: model.Paper{Key: p.Key}, State: "submitted",
		OutboundName: oldName, NamedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	f.g.files = []model.DriveFile{{ID: "export", Version: "1", Name: "reednd-quill - Kindle.pdf", CreatedTime: "2026-01-02T00:00:00Z"}}
	f.g.bytes["export"] = "%PDF-ink"
	f.run(t)
	if len(f.g.sent) != 0 || len(f.z.archives) != 1 || f.z.archives[0].parent != p.Key || f.jobs(t)[p.Key].OutboundName != oldName {
		t.Fatal(f.g.sent, f.z.archives, f.jobs(t)[p.Key])
	}
}
