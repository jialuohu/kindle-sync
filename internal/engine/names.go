package engine

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
	"kindle-sync/internal/model"
	"kindle-sync/internal/naming"
)

// Keep the delivery name stable once bytes have been prepared. A name saved by
// an older release without its original override must also remain usable.
func (r *round) deliveryName(ctx context.Context, j *model.Job, pdfYear string) (name string, provisional bool, err error) {
	if j.OutboundName != "" && (j.SourceHash != "" || j.ArtifactOverride == "") {
		return j.OutboundName, false, nil
	}
	if j.Token != "" {
		return legacyFilename(j.Token, j.Title), false, nil
	}
	if j.Year == "" && j.DOI != "" {
		j.Year = r.library.PublicationYear(ctx, j.DOI)
	}
	if j.Year == "" {
		j.Year = pdfYear
	}
	name, err = r.allocateName(j.Paper, cmp.Or(j.ArtifactOverride, r.options.ArtifactNames[j.Key]))
	return name, j.Year == "", err
}

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
		if job.Key == p.Key {
			continue
		}
		reserved[foldedName(job.OutboundName)] = true
		for _, sent := range job.PreviousDeliveries {
			reserved[foldedName(sent.OutboundName)] = true
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
	add := func(parent string, d model.Delivery) {
		if d.Token != "" {
			parents.add(d.Token, parent)
		}
		if d.OutboundName != "" {
			parents.add(foldedName(d.OutboundName), parent)
		}
	}
	for _, job := range r.jobs {
		add(job.Key, job.CurrentDelivery())
		for _, sent := range job.PreviousDeliveries {
			add(job.Key, sent)
		}
	}
	return parents
}

func (m parentIndex) delivery(file model.DriveFile, jobs map[string]model.Job) string {
	name := returnedName(file.Name)
	key, known := m[name]
	if !known {
		name = "" // A legacy token can identify a renamed file.
	}
	// Older exports may include extra words or turn spaces into underscores
	// around the original KRS token.
	tokens := tokenPattern.FindAllString(strings.ReplaceAll(strings.ToUpper(file.Name), "_", " "), -1)
	if len(tokens) > 1 {
		return ""
	}
	token := ""
	if len(tokens) == 1 {
		if legacy, ok := m[tokens[0]]; ok {
			if known && key != legacy {
				return ""
			}
			key, token = legacy, tokens[0]
		}
	}
	if key == "" {
		return ""
	}
	j := jobs[key]
	created, _ := time.Parse(time.RFC3339Nano, file.CreatedTime)
	matches := func(d model.Delivery) bool {
		if d.State != "submitted" && d.State != "confirmed_received" {
			return false
		}
		if name != "" && foldedName(d.OutboundName) != name || token != "" && token != d.Token {
			return false
		}
		if d.NamedAt == "" {
			// Older deliveries used a random token instead of a timestamp.
			return token != ""
		}
		named, err := time.Parse(time.RFC3339Nano, d.NamedAt)
		return err == nil && !created.IsZero() && created.After(named)
	}
	if matches(j.CurrentDelivery()) {
		return key
	}
	for _, sent := range j.PreviousDeliveries {
		if matches(sent) {
			return key
		}
	}
	return ""
}
