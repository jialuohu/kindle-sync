package engine

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
	"kindle-sync/internal/model"
	"kindle-sync/internal/naming"
)

func foldedName(name string) string {
	return norm.NFC.String(strings.ToLower(name))
}

// Scribe exports either the sent filename or the same stem followed by " - Kindle".
func returnedName(name string) string {
	stem, ok := strings.CutSuffix(foldedName(name), ".pdf")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(stem, " - kindle") + ".pdf"
}

func (r *round) allocateName(p model.Paper, artifact string) (string, error) {
	name, err := naming.Filename(p, artifact)
	if err != nil {
		return "", err
	}
	reserved := map[string]bool{}
	for _, job := range r.jobs {
		if job.Key != p.Key && job.OutboundName != "" {
			reserved[foldedName(job.OutboundName)] = true
		}
	}
	// An existing, unrelated or unmatched export must not acquire a parent
	// just because a newly queued paper would have the same filename.
	for _, rec := range r.receipts {
		if rec.Parent != p.Key {
			reserved[returnedName(rec.Name)] = true
		}
	}
	if !reserved[foldedName(name)] {
		return name, nil
	}
	stem := strings.TrimSuffix(name, ".pdf") + "-" + strings.ToLower(p.Key)
	name = stem + ".pdf"
	for n := 2; reserved[foldedName(name)]; n++ {
		name = fmt.Sprintf("%s-%d.pdf", stem, n)
	}
	return name, nil
}

// A duplicate name or legacy token in restored state is ambiguous.
type parentIndex map[string]string

func (m parentIndex) add(id, parent string) {
	if existing, found := m[id]; found && existing != parent {
		m[id] = ""
	} else {
		m[id] = parent
	}
}

func (r *round) deliveryParents() parentIndex {
	parents := parentIndex{}
	for _, job := range r.jobs {
		if job.Token != "" {
			parents.add(job.Token, job.Key)
		}
		switch job.State {
		case "sending", "submitted", "uncertain", "confirmed_received", "manual_upload":
			if job.OutboundName != "" {
				parents.add(foldedName(job.OutboundName), job.Key)
			}
		}
	}
	return parents
}

func (m parentIndex) lookup(name string) string {
	short, known := m[returnedName(name)]
	// Older exports may include extra words or turn spaces into underscores
	// around the original KRS token.
	tokens := tokenPattern.FindAllString(strings.ReplaceAll(strings.ToUpper(name), "_", " "), -1)
	switch len(tokens) {
	case 0:
		return short
	case 1:
		if legacy, ok := m[tokens[0]]; ok {
			if known && short != legacy {
				return ""
			}
			return legacy
		}
		return short
	default:
		return ""
	}
}

func canBeExport(j model.Job, file model.DriveFile) bool {
	if j.Token != "" || j.NamedAt == "" {
		return true // Older deliveries have no timestamp.
	}
	named, e1 := time.Parse(time.RFC3339Nano, j.NamedAt)
	created, e2 := time.Parse(time.RFC3339Nano, file.CreatedTime)
	return e1 == nil && e2 == nil && !created.Before(named)
}
