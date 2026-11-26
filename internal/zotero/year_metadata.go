package zotero

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

var publicationYear = regexp.MustCompile(`\b[1-9][0-9]{3}\b`)
var ordinaryDOI = regexp.MustCompile(`^10\.[0-9]{4,9}/[-._;()/:a-z0-9]+$`)
var arxivID = regexp.MustCompile(`^(?:([a-z]+(?:-[a-z]+)*(?:\.[a-z]{2})?)/)?([0-9]{2})(0[1-9]|1[0-2])(\.?)([0-9]{3,5})(?:v[1-9][0-9]*)?(?:\s+\[[a-z.-]+\])?$`)

// An identifier's YYMM component dates the first arXiv submission. Require
// the period-appropriate format, including the 2015 change to five digits.
func arxivSubmission(value string) (id, year string) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "arxiv:"))
	m := arxivID.FindStringSubmatch(value)
	if m == nil || strings.Trim(m[5], "0") == "" {
		return "", ""
	}
	month := m[2] + m[3]
	year = "20" + m[2]
	if m[1] == "" {
		digits := 4
		if month >= "1501" {
			digits = 5
		}
		if month < "0704" || year > time.Now().UTC().Format("2006") || m[4] != "." || len(m[5]) != digits {
			return "", ""
		}
	} else {
		if m[4] != "" || len(m[5]) != 3 || month > "0703" && month < "9107" {
			return "", ""
		}
		if month >= "9107" {
			year = "19" + m[2]
		}
	}
	id = m[2] + m[3] + m[4] + m[5]
	if m[1] != "" {
		id = m[1] + "/" + id
	}
	return id, year
}

func recognizedURL(raw string) (host, path string) {
	u, err := url.Parse(raw)
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil {
		return strings.ToLower(u.Host), strings.TrimPrefix(u.Path, "/")
	}
	return "", ""
}

func normalizeDOI(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimSpace(strings.TrimPrefix(s, "doi:"))
	if host, path := recognizedURL(s); host == "doi.org" || host == "dx.doi.org" {
		s = path
	}
	if ordinaryDOI.MatchString(s) {
		return s
	}
	return ""
}

type metadataValues map[string]bool

func (v metadataValues) add(s string) {
	if s != "" {
		v[s] = true
	}
}

func (v metadataValues) only() string {
	if len(v) == 1 {
		for s := range v {
			return s
		}
	}
	return ""
}

func (r record) publicationMetadata() (year, doi string) {
	dois, preprints, issued := metadataValues{}, metadataValues{}, metadataValues{}
	addArxiv := func(s string) {
		id, _ := arxivSubmission(s)
		preprints.add(id)
	}
	addDOI := func(s string) {
		d := normalizeDOI(s)
		if id, ok := strings.CutPrefix(d, "10.48550/arxiv."); ok {
			addArxiv(id)
		} else {
			dois.add(d)
		}
	}
	addDOI(r.Data.DOI)
	host, path := recognizedURL(r.Data.URL)
	switch host {
	case "doi.org", "dx.doi.org":
		addDOI(path)
	case "arxiv.org", "www.arxiv.org", "export.arxiv.org":
		page, id, _ := strings.Cut(strings.TrimSuffix(path, "/"), "/")
		switch page {
		case "abs", "html":
			addArxiv(id)
		case "pdf":
			addArxiv(strings.TrimSuffix(id, ".pdf"))
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Data.ArchiveID)), "arxiv:") {
		addArxiv(r.Data.ArchiveID)
	}
	for _, archive := range []string{r.Data.Archive, r.Data.Repository} {
		if strings.EqualFold(strings.TrimSpace(archive), "arXiv") || strings.EqualFold(strings.TrimSpace(archive), "arXiv.org") {
			addArxiv(r.Data.ArchiveID)
			addArxiv(r.Data.ArchiveLocation)
		}
	}
	for line := range strings.SplitSeq(r.Data.Extra, "\n") {
		label, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "issued", "date", "publication date":
			for _, y := range publicationYear.FindAllString(value, -1) {
				issued.add(y)
			}
		case "doi":
			addDOI(value)
		case "arxiv", "arxiv id":
			addArxiv(value)
		}
	}
	for _, date := range []string{r.Meta.ParsedDate, r.Data.Date} {
		years := publicationYear.FindAllString(date, 2)
		if len(years) > 1 {
			return "", "" // A range does not specify one publication year.
		}
		if len(years) == 1 {
			return years[0], dois.only()
		}
	}
	if len(issued) == 1 {
		return issued.only(), dois.only()
	}
	if len(issued) > 1 || len(dois) > 1 || len(preprints) > 1 {
		return "", ""
	}
	_, year = arxivSubmission(preprints.only())
	return year, dois.only()
}
