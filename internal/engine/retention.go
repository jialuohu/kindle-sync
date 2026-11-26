package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"kindle-sync/internal/model"
)

func exportTime(f model.DriveFile) time.Time {
	if modified, err := time.Parse(time.RFC3339Nano, f.ModifiedTime); err == nil {
		return modified
	}
	created, _ := time.Parse(time.RFC3339Nano, f.CreatedTime)
	return created
}

func laterExport(a, b model.DriveFile) bool {
	at, bt := exportTime(a), exportTime(b)
	if !at.Equal(bt) {
		return at.After(bt)
	}
	if a.ID != b.ID {
		return a.ID > b.ID
	}
	av, _ := strconv.ParseUint(a.Version, 10, 64)
	bv, _ := strconv.ParseUint(b.Version, 10, 64)
	return av > bv
}

// Recover legacy timestamps only from the identical Drive revision. When that
// file is gone, the time we observed the archive is an upper bound on its age.
func (r *round) recoverArchiveTimes(files []model.DriveFile, targetParent string) error {
	listed := make(map[revision]model.DriveFile, len(files))
	for _, f := range files {
		listed[revision{f.ID, f.Version}] = f
	}
	observed := time.Now().UTC().Format(time.RFC3339Nano)
	for k, rec := range r.receipts {
		if rec.State != "archived" || (r.options.ReturnFile != "" && rec.Parent != targetParent) {
			continue
		}
		previous := rec
		f := listed[k]
		if rec.CreatedTime == "" {
			rec.CreatedTime = f.CreatedTime
		}
		if rec.ModifiedTime == "" {
			rec.ModifiedTime = f.ModifiedTime
		}
		if exportTime(rec.DriveFile).IsZero() && rec.ObservedBefore == "" {
			rec.ObservedBefore = observed
		}
		if rec != previous {
			if err := r.saveReceipt(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// Different legacy file IDs cannot establish which was exported first.
// A new file created after observing an undated archive can safely replace it.
func compareNotes(a, b model.Receipt) (int, bool) {
	if a.ID == b.ID {
		av, ea := strconv.ParseUint(a.Version, 10, 64)
		bv, eb := strconv.ParseUint(b.Version, 10, 64)
		if ea == nil && eb == nil {
			return cmp.Compare(av, bv), true
		}
	}
	at, bt := exportTime(a.DriveFile), exportTime(b.DriveFile)
	if !at.IsZero() && !bt.IsZero() {
		if order := at.Compare(bt); order != 0 {
			return order, true
		}
		return cmp.Compare(a.ID, b.ID), true
	}
	if bound, err := time.Parse(time.RFC3339Nano, b.ObservedBefore); err == nil && bt.IsZero() {
		if created, err := time.Parse(time.RFC3339Nano, a.CreatedTime); err == nil && created.After(bound) {
			return 1, true
		}
	}
	if bound, err := time.Parse(time.RFC3339Nano, a.ObservedBefore); err == nil && at.IsZero() {
		if created, err := time.Parse(time.RFC3339Nano, b.CreatedTime); err == nil && created.After(bound) {
			return -1, true
		}
	}
	return 0, false
}

// Reduce each Drive file to its highest settled revision before comparing
// different files. An uploader can backdate modifiedTime on a newer revision.
type noteFiles map[string]model.Receipt

func (files noteFiles) add(x model.Receipt) {
	if head, found := files[x.ID]; !found || newerVersion(x.Version, head.Version) {
		files[x.ID] = x
	}
}

func (files noteFiles) olderRevision(x model.Receipt) bool {
	return newerVersion(files[x.ID].Version, x.Version)
}

func (files noteFiles) compare(x model.Receipt) (older, comparable bool) {
	if files.olderRevision(x) {
		return true, true
	}
	comparable = true
	for id, rec := range files {
		if id == x.ID || rec.State != "archived" {
			continue
		}
		order, known := compareNotes(x, rec)
		if known && order < 0 {
			return true, true
		}
		comparable = comparable && known
	}
	return false, comparable
}

func (files noteFiles) winner() (model.Receipt, bool) {
	// A dated export is a possible winner over undated records. The
	// second pass requires proof that every distinct child is older.
	var keep model.Receipt
	for _, rec := range files {
		if rec.State == "archived" && (keep.Child == "" || laterExport(rec.DriveFile, keep.DriveFile)) {
			keep = rec
		}
	}
	for _, rec := range files {
		if rec.State != "archived" || (rec.Child == keep.Child && rec.Hash == keep.Hash) {
			continue
		}
		if order, known := compareNotes(keep, rec); !known || order < 0 {
			return model.Receipt{}, false
		}
	}
	return keep, true
}

func (r *round) latestArchives() map[string]noteFiles {
	latest := make(map[string]noteFiles)
	for _, rec := range r.receipts {
		if rec.Parent == "" {
			continue
		}
		if latest[rec.Parent] == nil {
			latest[rec.Parent] = make(noteFiles)
		}
		if rec.State == "retired" || (rec.State == "archived" && rec.Child != "") {
			latest[rec.Parent].add(rec)
		}
	}
	return latest
}

// A conflicting local identity is an error even when the remote attachment
// happens to have the expected managed filename.
func (r *round) ownsNotes(rec model.Receipt) bool {
	if rec.Child == "" || !digestPattern.MatchString(rec.Hash) {
		return false
	}
	for _, j := range r.jobs {
		if j.Source == rec.Child {
			return false
		}
	}
	for _, old := range r.receipts {
		if old.Child == rec.Child && (old.Parent != rec.Parent || old.Hash != rec.Hash) {
			return false
		}
	}
	return true
}

func (r *round) retireOlderNotes(ctx context.Context, latest map[string]noteFiles, targetParent string) error {
	winners := make(map[string]model.Receipt)
	for parent, files := range latest {
		if r.options.ReturnFile != "" && parent != targetParent {
			continue
		}
		keep, ok := files.winner()
		if !ok {
			r.report.Problems = append(r.report.Problems, "cannot order undated Kindle notes for "+parent)
			continue
		}
		winners[parent] = keep
	}
	trashed := make(map[string]bool)
	for _, rec := range r.receipts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.options.ReturnFile != "" && rec.Parent != targetParent {
			continue
		}
		keep := winners[rec.Parent]
		if keep.Child == "" || rec.Child == "" || (rec.State != "archived" && rec.State != "pending") {
			continue
		}
		order, known := compareNotes(keep, rec)
		if !latest[rec.Parent].olderRevision(rec) && (!known || order <= 0) {
			continue
		}
		if !r.ownsNotes(rec) || !r.ownsNotes(keep) {
			r.report.Problems = append(r.report.Problems, "conflicting notes attachment identity for "+rec.Parent)
			continue
		}
		if r.options.DryRun {
			if rec.Child != keep.Child {
				r.report.Actions = append(r.report.Actions, "would retire older notes: "+rec.Child)
			}
			continue
		}
		if rec.Child != keep.Child && !trashed[rec.Child] {
			if err := r.library.RetireNotes(ctx, rec.Parent, keep, rec); err != nil {
				if r.options.ReturnFile != "" || !errors.Is(err, model.ErrParentGone) {
					r.report.Problems = append(r.report.Problems, fmt.Sprintf("retiring older notes for %s: %v", rec.Parent, err))
				}
				continue
			}
			trashed[rec.Child] = true
			r.report.Actions = append(r.report.Actions, "retired older notes: "+rec.Child)
		}
		// A crash can leave cached bytes after a completed or partial upload.
		// The recorded child key survives an interruption during cleanup.
		if err := removeCachedPDF(r.receiptPath(rec)); err != nil {
			return err
		}
		rec.State, rec.Error = "retired", ""
		if err := r.saveReceipt(rec); err != nil {
			return err
		}
	}
	return nil
}
