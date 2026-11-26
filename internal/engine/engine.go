// Package engine runs one serialized round of paper delivery and annotation intake.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
	if options.MatchFile == "" {
		if err := r.outbound(ctx); err != nil {
			return r.report, err
		}
	}
	if options.SkipReturns {
		if options.MatchFile != "" {
			return r.report, errors.New("bind a Drive folder before matching an export")
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
		r.report.Problems = append(r.report.Problems, "reading Zotero collection: "+err.Error())
	} else if !initial {
		if !r.options.DryRun {
			if err := r.store.Seed(members); err != nil {
				return err
			}
		}
		for _, p := range members {
			r.jobs[p.Key] = model.Job{Paper: p, State: "baseline"}
		}
		r.report.Actions = append(r.report.Actions, fmt.Sprintf("baseline: %d papers", len(members)))
	} else {
		for _, p := range members {
			if _, ok := r.jobs[p.Key]; !ok {
				if err := r.saveJob(model.Job{Paper: p, State: "queued"}); err != nil {
					return err
				}
			}
		}
	}
	if r.options.Backfill != "" {
		if err != nil {
			return fmt.Errorf("cannot backfill without a complete reading queue: %w", err)
		}
		eligible := make(map[string]bool, len(members))
		for _, paper := range members {
			eligible[paper.Key] = true
		}
		members, err := r.library.Members(ctx, r.options.Backfill)
		if err != nil {
			return err
		}
		for _, p := range members {
			if !eligible[p.Key] {
				continue
			}
			j, ok := r.jobs[p.Key]
			if !ok || j.State == "baseline" {
				if err := r.saveJob(model.Job{Paper: p, State: "queued"}); err != nil {
					return err
				}
			}
		}
	}
	if r.options.Item != "" {
		j, ok := r.jobs[r.options.Item]
		if !ok {
			return errors.New("paper was not discovered in the configured reading queue")
		}
		switch j.State {
		case "queued", "waiting_pdf", "needs_source", "baseline":
			j.Source, j.State, j.Error = r.options.Attachment, "queued", ""
			if err := r.saveJob(j); err != nil {
				return err
			}
		default:
			return fmt.Errorf("cannot override attachment of paper in state %s", j.State)
		}
	}
	keys := make([]string, 0, len(r.jobs))
	for key := range r.jobs {
		keys = append(keys, key)
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
			if r.options.Item != "" && key != r.options.Item {
				continue
			}
			if err := r.deliver(ctx, j); err != nil {
				return err
			}
		}
	}
	return nil
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
	if r.options.DryRun {
		r.report.Actions = append(r.report.Actions, fmt.Sprintf("would prepare %s from attachment %s", j.Key, source.Key))
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
	if j.Token == "" {
		j.Token, err = newToken()
		if err != nil {
			return err
		}
	}
	j.Source, j.SourceHash, j.Error, j.Candidates = source.Key, hash, "", nil
	name := filename(j.Token, j.Title)
	if size > r.options.MaxEmailBytes {
		j.State = "manual_upload"
		j.ManualFile = filepath.Join(r.options.Spool, "manual", name)
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
	id, err := r.google.Send(ctx, path, name)
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

// Resolve records an operator's device check after Gmail's response was lost.
// A retry remains queued until the next sync, which uses the original delivery code.
func Resolve(store model.Store, key, decision string) (model.Job, error) {
	if decision != "received" && decision != "retry" {
		return model.Job{}, errors.New("decision must be received or retry")
	}
	jobs, err := store.Jobs()
	if err != nil {
		return model.Job{}, err
	}
	for _, job := range jobs {
		if job.Key != key {
			continue
		}
		if job.State != "uncertain" && job.State != "sending" {
			return job, errors.New("only an uncertain or interrupted submission can be resolved")
		}
		if decision == "retry" {
			job.State = "queued"
		} else {
			job.State = "confirmed_received"
		}
		job.Error = ""
		return job, store.PutJob(job)
	}
	return model.Job{}, errors.New("unknown paper key")
}
