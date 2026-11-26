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
	// Honor names already pinned to other parents or held by older releases.
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
		if job.OutboundName != "" {
			parents.add(foldedName(job.OutboundName), job.Key)
		}
	}
	return parents
}

func (m parentIndex) delivery(file model.DriveFile, jobs map[string]model.Job) string {
	key, known := m[returnedName(file.Name)]
	// Older exports may include extra words or turn spaces into underscores
	// around the original KRS token.
	tokens := tokenPattern.FindAllString(strings.ReplaceAll(strings.ToUpper(file.Name), "_", " "), -1)
	if len(tokens) > 1 {
		return ""
	}
	if len(tokens) == 1 {
		if legacy, ok := m[tokens[0]]; ok {
			if known && key != legacy {
				return ""
			}
			key = legacy
		}
	}
	j := jobs[key]
	if j.State != "submitted" && j.State != "confirmed_received" {
		return ""
	}
	if j.NamedAt == "" {
		// Older deliveries used a random token instead of a timestamp.
		if len(tokens) == 1 && tokens[0] == j.Token {
			return key
		}
		return ""
	}
	named, e1 := time.Parse(time.RFC3339Nano, j.NamedAt)
	created, e2 := time.Parse(time.RFC3339Nano, file.CreatedTime)
	if e1 == nil && e2 == nil && created.After(named) {
		return key
	}
	return ""
}
