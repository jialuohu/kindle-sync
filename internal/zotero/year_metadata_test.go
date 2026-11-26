package zotero

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPublicationYearFallbacks(t *testing.T) {
	const doi = "10.1234/example.42"
	for _, tc := range []struct{ name, data, year, doi, added string }{
		{"Zotero date wins", `{"date":"June 2023","extra":"Issued: 2024\narXiv: 2203.01234"}`, "2023", "", ""},
		{"extra issued", `{"extra":"Accessed: 2025-01-01\nIssued: 2024-02-03\nDOI: doi:10.1234/Example.42","url":"https://arxiv.org/abs/2301.01234"}`, "2024", doi, ""},
		{"arxiv archive", `{"repository":"arXiv","archiveID":"2503.03124v2","extra":"arXiv: 2503.03124 [cs.DC]"}`, "2025", "", ""},
		{"arxiv pdf", `{"url":"https://arxiv.org/pdf/1412.0093v2.pdf?download=1"}`, "2014", "", ""},
		{"legacy arxiv DOI", `{"DOI":"10.48550/ARXIV.HEP-TH/9901001"}`, "1999", "", ""},
		{"old arxiv archive location", `{"archive":"arXiv.org","archiveLocation":"math.GT/0301001"}`, "2003", "", ""},
		{"ordinary DOI", `{"url":"https://doi.org/10.1234/Example.42","DOI":"10.1234/EXAMPLE.42"}`, "", doi, ""},
		{"import year only", `{"dateAdded":"2026-01-02T23:50:30Z","dateModified":"2025-04-15","extra":"Accessed: 2023","title":"Survey 2024"}`, "", "", "2026"},
		{"invalid import date", `{"dateAdded":"2025-02-29T00:00:00Z"}`, "", "", ""},
		{"ambiguous date", `{"date":"2022–2024","DOI":"10.1234/example.42","extra":"Issued: 2023\narXiv: 2201.01234"}`, "", "", ""},
		{"conflicting issue dates", `{"extra":"Issued: 2023\nPublication Date: 2022\narXiv: 2201.01234","DOI":"10.1234/example.42"}`, "", "", ""},
		{"different arxiv IDs", `{"extra":"arXiv: 2201.01234","url":"https://arxiv.org/abs/2201.01235"}`, "", "", ""},
		{"unrelated archive", `{"archiveID":"2201.01234","repository":"Example Archive","url":"https://arxiv.org.example.invalid/abs/2201.01234"}`, "", "", ""},
		{"conflicting DOIs", `{"DOI":"10.1234/example.42","extra":"DOI: 10.1234/example.43"}`, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r record
			if err := json.Unmarshal([]byte(tc.data), &r.Data); err != nil {
				t.Fatal(err)
			}
			p := r.paper()
			if p.Year != tc.year || p.DOI != tc.doi || p.AddedYear != tc.added {
				t.Fatalf("got year %q DOI %q added year %q", p.Year, p.DOI, p.AddedYear)
			}
		})
	}
}

func TestInvalidArxivAndDOI(t *testing.T) {
	future := time.Now().UTC().AddDate(1, 0, 0).Format("06") + "01.01234"
	for _, bad := range []string{future, "0703.1001", "1412.00001", "1501.1200", "1513.01234", "2501.00000", "hep-th/0801123", "hep-th/9001123", "reference 2401.12345"} {
		if _, y := arxivSubmission(bad); y != "" {
			t.Errorf("unexpected arXiv year %q for %q", y, bad)
		}
	}
	for _, bad := range []string{"https://doi.org.example.invalid/10.1234/example", "https://user@doi.org/10.1234/example", "10.1234/example?token=example", "10.1234/example another title"} {
		if d := normalizeDOI(bad); d != "" {
			t.Errorf("unexpected DOI %q for %q", d, bad)
		}
	}
}
