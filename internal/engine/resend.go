package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"kindle-sync/internal/model"
)

// A renamed copy is an explicit new delivery of an already submitted source.
func (r *round) resend(ctx context.Context, members []model.Paper) error {
	key, expected := r.options.Item, r.options.ExpectedResendName
	i := slices.IndexFunc(members, func(p model.Paper) bool { return p.Key == key })
	if i < 0 {
		return errors.New("paper is not in the configured reading collection")
	}
	previous, ok := r.jobs[key]
	if !ok {
		return errors.New("paper has no recorded delivery")
	}
	deliveries := append(slices.Clone(previous.PreviousDeliveries), previous.CurrentDelivery())
	for _, d := range deliveries {
		if (d.State == "submitted" || d.State == "confirmed_received") && d.OutboundName == expected {
			r.report.Actions = append(r.report.Actions, fmt.Sprintf("%s already sent as %s", key, expected))
			return nil
		}
	}
	if previous.State != "submitted" && previous.State != "confirmed_received" {
		return fmt.Errorf("cannot resend a paper in state %s", previous.State)
	}
	sources, err := r.library.Sources(ctx, key)
	if err != nil {
		return err
	}
	original := slices.IndexFunc(sources, func(a model.Attachment) bool { return a.Key == previous.Source && a.Key != "" })
	if original < 0 {
		return errors.New("original source attachment is no longer available")
	}
	source := sources[original]
	job := model.Job{Paper: members[i], Source: source.Key, RejectedGmailIDs: previous.RejectedGmailIDs}
	if r.options.DryRun {
		name, provisional, err := r.deliveryName(ctx, &job, "")
		if err != nil {
			return err
		}
		if !provisional && name != expected {
			return fmt.Errorf("computed filename %q differs from requested %q", name, expected)
		}
		action := fmt.Sprintf("would resend %s as %s after verifying the original PDF", key, expected)
		if provisional {
			action += fmt.Sprintf(" (metadata suggests %s; the PDF will be checked on send)", name)
		}
		r.report.Actions = append(r.report.Actions, action)
		return nil
	}

	dir, err := tempDir(r.options.Spool)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "paper.pdf")
	year, hash, size, err := r.downloadPDF(ctx, source, path)
	if err != nil {
		return err
	}
	if previous.SourceHash != "" && previous.SourceHash != hash {
		return errors.New("original source attachment contents have changed")
	}
	if size > r.options.MaxEmailBytes {
		return errors.New("original PDF exceeds the email size limit")
	}
	name, _, err := r.deliveryName(ctx, &job, year)
	if err != nil {
		return err
	}
	if name != expected {
		return fmt.Errorf("computed filename %q differs from requested %q", name, expected)
	}
	job.OutboundName, job.SourceHash = name, hash
	job.NamedAt = time.Now().UTC().Format(time.RFC3339Nano)
	job.PreviousDeliveries = deliveries
	return r.submit(ctx, job, path, &previous)
}
