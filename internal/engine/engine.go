// Package engine runs one serialized round of paper delivery and annotation intake.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"kindle-sync/internal/model"
)

type round struct {
	store    model.Store
	library  model.Zotero
	google   model.Google
	options  model.Options
	jobs     map[string]model.Job
	receipts map[revision]model.Receipt
	report   model.Report
}
type revision struct{ id, version string }

// Run requires an exclusive process lock held by the caller through the final store write.
func Run(ctx context.Context, store model.Store, library model.Zotero, google model.Google, options model.Options) (model.Report, error) {
	r := &round{
		store: store, library: library, google: google, options: options,
		jobs: make(map[string]model.Job), receipts: make(map[revision]model.Receipt),
		report: model.Report{Actions: []string{}, Problems: []string{}},
	}
	jobs, err := store.Jobs()
	if err != nil {
		return r.report, err
	}
	for _, j := range jobs {
		r.jobs[j.Key] = j
	}
	receipts, err := store.Receipts()
	if err != nil {
		return r.report, err
	}
	for _, rec := range receipts {
		r.receipts[revision{rec.ID, rec.Version}] = rec
	}
	direction := options.SyncDirection()
	if direction != "receive" {
		if err := r.outbound(ctx); err != nil {
			return r.report, err
		}
	}
	if direction == "send" {
		return r.report, nil
	}
	if options.SkipReturns {
		if direction == "receive" {
			return r.report, errors.New("bind a Drive folder before processing an export")
		}
		r.report.Actions = append(r.report.Actions, "Drive folder not yet bound; scanning for returns is paused")
		return r.report, nil
	}
	err = r.inbound(ctx)
	return r.report, err
}

func (r *round) saveJob(j model.Job) error {
	if !r.options.DryRun {
		if err := r.store.PutJob(j); err != nil {
			return err
		}
	}
	r.jobs[j.Key] = j
	return nil
}

func (r *round) saveReceipt(x model.Receipt) error {
	if !r.options.DryRun {
		if err := r.store.PutReceipt(x); err != nil {
			return err
		}
	}
	r.receipts[revision{x.ID, x.Version}] = x
	return nil
}

func (r *round) outbound(ctx context.Context) error {
	initial, err := r.store.Initialized()
	if err != nil {
		return err
	}
	members, err := r.library.Members(ctx, r.options.Root)
	if err != nil {
		if r.options.Item != "" || r.options.Backfill != "" {
			return fmt.Errorf("cannot verify the requested papers in the reading collection: %w", err)
		}
		r.report.Problems = append(r.report.Problems, "reading Zotero collection: "+err.Error())
		return nil // Continue Drive intake, but never send from a stale membership snapshot.
	}
	selected, err := r.selection(ctx, members)
	if err != nil {
		return err
	}
	if !initial {
		if !r.options.DryRun {
			if err := r.store.Seed(members); err != nil {
				return err
			}
		}
		for _, p := range members {
			r.jobs[p.Key] = model.Job{Paper: p, State: "baseline"}
		}
		r.report.Actions = append(r.report.Actions, fmt.Sprintf("baseline: %d papers", len(members)))
	}
	for _, p := range members {
		if !selected[p.Key] {
			continue
		}
		if err := r.discover(p); err != nil {
			return err
		}
	}
	if r.options.Item != "" {
		j, ok := r.jobs[r.options.Item]
		if !ok {
			return errors.New("paper was not discovered in the configured reading queue")
		}
		switch j.State {
		case "queued", "waiting_pdf", "needs_source", "baseline":
			if r.options.Artifact != "" {
				if j.Token != "" {
					return errors.New("retry uses the original delivery token and filename")
				}
				name, err := r.allocateName(j.Paper, r.options.Artifact)
				if err != nil {
					return err
				}
				if j.SourceHash != "" && j.OutboundName != name {
					return fmt.Errorf("retry uses the original filename %s", j.OutboundName)
				}
				j.OutboundName = name
			}
			if r.options.Attachment != "" {
				j.Source = r.options.Attachment
			}
			j.State, j.Error = "queued", ""
			if err := r.saveJob(j); err != nil {
				return err
			}
		case "submitted", "confirmed_received", "manual_upload":
			if r.options.Attachment != "" || r.options.Artifact != "" {
				return fmt.Errorf("cannot override attachment of paper in state %s", j.State)
			}
			r.report.Actions = append(r.report.Actions, fmt.Sprintf("%s already in state %s", j.Key, j.State))
			return nil
		case "sending":
			// The delivery loop below records the interruption without submitting again.
		default:
			return fmt.Errorf("cannot override attachment of paper in state %s", j.State)
		}
	}
	keys := make([]string, 0, len(r.jobs))
	for key, job := range r.jobs {
		// A full queue scan may recover an interrupted Gmail attempt after the
		// paper was removed. Only currently selected papers can call Send.
		recoverInterrupted := job.State == "sending" && r.options.Item == "" && r.options.Backfill == ""
		if selected[key] || recoverInterrupted {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		j := r.jobs[key]
		switch j.State {
		case "sending":
			// A process disappeared between the durable intent and the durable Gmail response.
			j.State, j.Error = "uncertain", "previous submission was interrupted; check the Kindle library before retrying"
			if err := r.saveJob(j); err != nil {
				return err
			}
			r.report.Problems = append(r.report.Problems, j.Key+": "+j.Error)
		case "queued", "needs_source", "waiting_pdf":
			if err := r.deliver(ctx, j); err != nil {
				return err
			}
		}
	}
	return nil
}

// Every send is limited to the current watched tree, including delayed retries.
func (r *round) selection(ctx context.Context, members []model.Paper) (map[string]bool, error) {
	watched := make(map[string]bool, len(members))
	for _, p := range members {
		watched[p.Key] = true
	}
	if key := r.options.Item; key != "" {
		if watched[key] {
			return map[string]bool{key: true}, nil
		}
		return nil, errors.New("paper is not in the configured reading collection")
	}
	if r.options.Backfill == "" || r.options.Backfill == r.options.Root {
		return watched, nil
	}
	collection, err := r.library.Members(ctx, r.options.Backfill)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool)
	for _, p := range collection {
		if watched[p.Key] {
			selected[p.Key] = true
		}
	}
	return selected, nil
}

func (r *round) discover(p model.Paper) error {
	j, exists := r.jobs[p.Key]
	if !exists {
		return r.saveJob(model.Job{Paper: p, State: "queued"})
	}
	switch j.State {
	case "queued", "waiting_pdf", "needs_source", "baseline":
	default:
		return nil
	}
	before, state := j.Paper, j.State
	if j.Token == "" && j.SourceHash == "" {
		j.Paper = p
	}
	if j.State == "baseline" && r.options.Backfill != "" {
		j.State = "queued"
	}
	if j.Paper == before && j.State == state {
		return nil
	}
	return r.saveJob(j)
}

func (r *round) problem(j model.Job, state string, err error) error {
	j.State, j.Error = state, err.Error()
	r.report.Problems = append(r.report.Problems, j.Key+": "+j.Error)
	return r.saveJob(j)
}

func (r *round) deliver(ctx context.Context, j model.Job) error {
	sources, err := r.library.Sources(ctx, j.Key)
	if err != nil {
		return r.problem(j, "waiting_pdf", err)
	}
	j.Candidates = sources
	var source model.Attachment
	if j.Source != "" {
		for _, a := range sources {
			if a.Key == j.Source {
				source = a
				break
			}
		}
		if source.Key == "" {
			return r.problem(j, "needs_source", errors.New("selected attachment is not an available source"))
		}
	} else {
		switch len(sources) {
		case 0:
			return r.problem(j, "waiting_pdf", errors.New("waiting for a stored PDF attachment"))
		case 1:
			source = sources[0]
		default:
			return r.problem(j, "needs_source", errors.New("choose one of the candidate PDFs"))
		}
	}
	name := j.OutboundName
	if name == "" {
		if j.Token != "" {
			name = legacyFilename(j.Token, j.Title)
		} else {
			name, err = r.allocateName(j.Paper, r.options.ArtifactNames[j.Key])
			if err != nil {
				return err
			}
		}
	}
	if r.options.DryRun {
		j.OutboundName = name
		r.jobs[j.Key] = j // Reserve the preview name for the remaining items in this round.
		r.report.Actions = append(r.report.Actions, fmt.Sprintf("would prepare %s from attachment %s as %s", j.Key, source.Key, j.OutboundName))
		return nil
	}
	dir, err := tempDir(r.options.Spool)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "paper.pdf")
	if err := r.library.Download(ctx, source, path); err != nil {
		if errors.Is(err, model.ErrTooLarge) {
			return r.problem(j, "too_large", err)
		}
		return r.problem(j, "waiting_pdf", err)
	}
	hash, size, err := pdfInfo(path, source.MD5)
	if err != nil {
		return r.problem(j, "waiting_pdf", err)
	}
	j.Source, j.SourceHash, j.Error, j.Candidates = source.Key, hash, "", nil
	j.OutboundName = name
	if j.Token == "" {
		// A rejected or interrupted attempt may be retried much later. A
		// same-name PDF created before this attempt cannot be its export.
		j.NamedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if size > r.options.MaxEmailBytes {
		// Reserve the name before publishing the file so an interrupted run
		// cannot assign it to a different paper on the next scan.
		if err := r.saveJob(j); err != nil {
			return err
		}
		j.State = "manual_upload"
		j.ManualFile = filepath.Join(r.options.Spool, "manual", j.OutboundName)
		if err := keepFile(path, j.ManualFile); err != nil {
			return err
		}
		if size > webLimit {
			return r.problem(j, "too_large", errors.New("PDF exceeds the 200 MB Send to Kindle web limit"))
		}
		r.report.Actions = append(r.report.Actions, "manual upload: "+j.Key+" "+j.ManualFile)
		return r.saveJob(j)
	}
	j.State = "sending"
	if err := r.saveJob(j); err != nil {
		return err
	}
	id, err := r.google.Send(ctx, path, j.OutboundName)
	if err != nil {
		if errors.Is(err, model.ErrSendRejected) {
			return r.problem(j, "queued", err)
		}
		return r.problem(j, "uncertain", err)
	}
	if id == "" {
		return r.problem(j, "uncertain", errors.New("Gmail submission returned no message ID"))
	}
	j.GmailID, j.State = id, "submitted"
	r.report.Actions = append(r.report.Actions, "submitted "+j.Key+" to Gmail")
	return r.saveJob(j)
}

// Resolve records a device check or a confirmed Amazon rejection. An explicit
// retry waits for the next sync and keeps the original delivery token.
func Resolve(store model.Store, key, decision, gmailID string) (model.Job, error) {
	if decision != "received" && decision != "retry" && decision != "rejected" {
		return model.Job{}, errors.New("decision must be received, retry, or rejected")
	}
	if (decision == "rejected") != (gmailID != "") {
		return model.Job{}, errors.New("supply the Gmail message ID only when confirming a rejection")
	}
	jobs, err := store.Jobs()
	if err != nil {
		return model.Job{}, err
	}
	for _, job := range jobs {
		if job.Key != key {
			continue
		}
		if decision == "rejected" {
			if job.State != "submitted" || job.GmailID != gmailID {
				return job, errors.New("rejection must match the current submitted Gmail message ID")
			}
			job.RejectedGmailIDs = append(job.RejectedGmailIDs, job.GmailID)
			job.GmailID = ""
			job.State = "queued"
		} else {
			if job.State != "uncertain" && job.State != "sending" {
				return job, errors.New("only an uncertain or interrupted submission can be resolved as received or retried")
			}
			if decision == "received" {
				job.State = "confirmed_received"
			} else {
				job.State = "queued"
			}
		}
		job.Error = ""
		return job, store.PutJob(job)
	}
	return model.Job{}, errors.New("unknown paper key")
}
