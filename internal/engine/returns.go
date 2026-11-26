package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"kindle-sync/internal/model"
)

func (r *round) inbound(ctx context.Context) error {
	files, err := r.google.Files(ctx)
	if err != nil {
		r.report.Problems = append(r.report.Problems, "listing Drive exports: "+err.Error())
	}
	current := make(map[revision]bool)
	listedVersions := make(map[string]string)
	listComplete := err == nil
	parents := r.deliveryParents()
	for _, f := range files {
		if r.options.ReturnFile != "" && f.ID != r.options.ReturnFile {
			continue
		}
		if f.ID == "" || f.Version == "" {
			return errors.New("Drive file is missing identity or revision")
		}
		key := revision{f.ID, f.Version}
		if seen := listedVersions[f.ID]; seen != "" && seen != f.Version {
			return errors.New("Drive file changed during listing; retry the scan")
		}
		current[key] = true
		listedVersions[f.ID] = f.Version
		if _, ok := r.receipts[key]; ok {
			continue
		}
		parent := parents.delivery(f, r.jobs)
		if parent != "" || r.options.MatchParent != "" {
			r.receipts[key] = model.Receipt{DriveFile: f, Parent: parent, State: "pending"}
		}
	}
	var target revision
	if r.options.ReturnFile != "" {
		if err != nil && r.options.MatchVersion == "" {
			return fmt.Errorf("cannot identify current Drive revision: %w", err)
		}
		if r.options.MatchParent != "" {
			target, err = r.matchTarget(current)
			if err != nil {
				return err
			}
		} else {
			version, ok := listedVersions[r.options.ReturnFile]
			if !ok {
				return errors.New("current PDF not found in the bound Drive folder")
			}
			target = revision{r.options.ReturnFile, version}
			if _, found := r.receipts[target]; !found {
				r.report.Actions = append(r.report.Actions, "left on Drive: no matching delivery for "+r.options.ReturnFile)
				return nil
			}
		}
	}
	targetParent := r.receipts[target].Parent
	if r.options.MatchParent != "" {
		targetParent = r.options.MatchParent
	}
	if err := r.recoverArchiveTimes(files, targetParent); err != nil {
		return err
	}
	work := make([]revision, 0)
	for k, rec := range r.receipts {
		if r.options.ReturnFile != "" && k != target {
			continue
		}
		if rec.State == "retired" {
			if r.options.ReturnFile != "" {
				r.report.Actions = append(r.report.Actions, "newer notes retained: "+rec.ID)
			}
			continue
		}
		if rec.State == "archived" {
			if r.options.ReturnFile != "" {
				r.report.Actions = append(r.report.Actions, "already archived: "+rec.ID)
			}
			continue
		}
		// Honor the pinned parent on retries. Older releases also cached
		// unassigned PDFs or matched jobs that had never been sent.
		if r.options.MatchParent == "" && (rec.Parent == "" || parents.delivery(rec.DriveFile, r.jobs) != rec.Parent) {
			if r.options.ReturnFile != "" {
				r.report.Actions = append(r.report.Actions, "left on Drive: no matching delivery for "+rec.ID)
			}
			continue
		}
		if listComplete && rec.Hash == "" && rec.Child == "" && newerVersion(listedVersions[k.id], rec.Version) {
			if r.options.ReturnFile != "" {
				return errors.New("Drive replaced this revision before it could be downloaded")
			}
			if rec.State != "superseded" {
				rec.State, rec.Error = "superseded", "Drive replaced this revision before it could be downloaded"
				if err := r.saveReceipt(rec); err != nil {
					return err
				}
				r.report.Actions = append(r.report.Actions, "superseded export: "+rec.ID+" version "+rec.Version)
			}
			continue
		}
		if rec.State == "superseded" && !current[k] {
			if r.options.ReturnFile != "" {
				return errors.New(rec.Error)
			}
			continue
		}
		work = append(work, k)
	}
	latest := r.latestArchives()
	sort.Slice(work, func(i, j int) bool {
		a, b := r.receipts[work[i]], r.receipts[work[j]]
		if a.Parent != b.Parent {
			return a.Parent < b.Parent
		}
		// Apply replacements to known Drive files before ranking new IDs.
		// Their modification timestamp may have moved in either direction.
		headA, headB := latest[a.Parent][a.ID], latest[b.Parent][b.ID]
		updateA := headA.ID != "" && newerVersion(a.Version, headA.Version)
		updateB := headB.ID != "" && newerVersion(b.Version, headB.Version)
		if updateA != updateB {
			return updateA
		}
		return laterExport(a.DriveFile, b.DriveFile)
	})
	failed := make(map[string]bool)
	for _, k := range work {
		if err := ctx.Err(); err != nil {
			return err
		}
		x := r.receipts[k]
		if r.options.MatchParent != "" {
			x.Parent = r.options.MatchParent
		}
		if failed[x.Parent] {
			continue
		}
		ok, err := r.canArchiveToParent(ctx, x.Parent)
		if err != nil {
			if r.options.ReturnFile != "" {
				return err
			}
			r.report.Problems = append(r.report.Problems, x.ID+": "+err.Error())
			continue
		}
		if !ok {
			continue
		}
		notes := latest[x.Parent]
		if notes == nil { // A manual match may assign a previously unknown parent.
			notes = make(noteFiles)
			latest[x.Parent] = notes
		}
		older, comparable := notes.compare(x)
		keep, _ := notes.winner()
		if !comparable {
			r.report.Problems = append(r.report.Problems, "cannot order export against undated notes for "+x.Parent)
			continue
		}
		if older {
			if x.Child != "" {
				continue // The retention pass can retire this unfinished upload.
			}
			if x.Hash == "" {
				x.State, x.Error = "retired", ""
				if err := r.saveReceipt(x); err != nil {
					return err
				}
				notes.add(x)
				r.report.Actions = append(r.report.Actions, "newer notes retained: "+x.ID)
				continue
			}
			// The remote write may have succeeded before its child key was
			// saved. Recover it by content before retiring it.
		}
		if r.options.DryRun {
			r.report.Actions = append(r.report.Actions, "would reconcile export for "+x.Parent+": "+x.ID)
			// Model the successful archive for the rest of this preview.
			x.State = "archived"
			notes.add(x)
			continue
		}
		if err := r.receive(ctx, x, keep, current[k]); err != nil {
			return err
		}
		saved := r.receipts[k]
		if saved.State != "archived" {
			failed[x.Parent] = true
		} else {
			notes.add(saved)
		}
	}
	return r.retireOlderNotes(ctx, latest, targetParent)
}

// Drive's monotonically increasing version is a decimal integer encoded as a string.
func newerVersion(candidate, previous string) bool {
	cur, e1 := strconv.ParseUint(candidate, 10, 64)
	old, e2 := strconv.ParseUint(previous, 10, 64)
	return e1 == nil && e2 == nil && cur > old
}

// A file ID can have many revisions with different PDFs. An explicit match applies
// to exactly one revision. Held, superseded versions remain addressable by version.
func (r *round) matchTarget(current map[revision]bool) (revision, error) {
	if r.options.MatchParent == "" || r.options.DryRun {
		return revision{}, errors.New("match requires a parent key and a writable run")
	}
	var candidates []revision
	for key := range r.receipts {
		if key.id != r.options.ReturnFile {
			continue
		}
		if r.options.MatchVersion != "" {
			if key.version == r.options.MatchVersion {
				candidates = []revision{key}
				break
			}
		} else if current[key] {
			candidates = []revision{key}
			break
		} else {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 {
		return revision{}, errors.New("export revision not found in the bound Drive folder or cached receipts")
	}
	if len(candidates) > 1 {
		return revision{}, errors.New("multiple cached revisions; specify the Drive version to match")
	}
	key := candidates[0]
	if parent := r.receipts[key].Parent; parent != "" && parent != r.options.MatchParent {
		return revision{}, errors.New("export revision already belongs to another parent")
	}
	return key, nil
}

func (r *round) receive(ctx context.Context, x, keep model.Receipt, current bool) error {
	path, err := r.materialize(ctx, &x, current)
	if err != nil {
		return r.receiptProblem(x, err)
	}
	x.State, x.Error = "pending", ""
	// Save the identity of the parent and bytes before a potentially committing remote write.
	if err := r.saveReceipt(x); err != nil {
		return err
	}
	// Retired children may still be in Zotero trash. Only the current
	// verified attachment can satisfy a later export of the same bytes.
	if keep.Child != "" && keep.Hash == x.Hash && (x.Child == "" || x.Child == keep.Child) {
		x.Child = keep.Child
		return r.archived(x, path, "already archived: "+x.ID)
	}
	child, err := r.library.Archive(ctx, x.Parent, path, x.Child)
	if child != "" {
		x.Child = child
	}
	if err != nil {
		return r.receiptProblem(x, err)
	}
	if child == "" {
		return r.receiptProblem(x, errors.New("archive returned no verified child attachment key"))
	}
	return r.archived(x, path, "archived "+x.ID+" under "+x.Parent+" as "+x.Child)
}

func (r *round) receiptProblem(x model.Receipt, err error) error {
	x.Error = err.Error()
	r.report.Problems = append(r.report.Problems, x.ID+": "+x.Error)
	return r.saveReceipt(x)
}

func (r *round) archived(x model.Receipt, path, action string) error {
	x.State, x.Error = "archived", ""
	if err := r.saveReceipt(x); err != nil {
		return err
	}
	if err := removeCachedPDF(path); err != nil {
		return err
	}
	r.report.Actions = append(r.report.Actions, action)
	return nil
}

func removeCachedPDF(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Dir(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (r *round) receiptPath(x model.Receipt) string {
	key := sha256.Sum256([]byte(x.ID + "\x00" + x.Version))
	return filepath.Join(r.options.Spool, "returns", hex.EncodeToString(key[:]), "Kindle Notes - "+x.Hash+".pdf")
}

// Keep bytes of unfinished receipts so later exports can supersede their Drive revisions.
func (r *round) materialize(ctx context.Context, x *model.Receipt, current bool) (string, error) {
	if x.Hash != "" {
		if !digestPattern.MatchString(x.Hash) {
			return "", errors.New("invalid cached PDF hash")
		}
		path := r.receiptPath(*x)
		hash, _, err := pdfInfo(path, x.MD5)
		if err == nil && hash == x.Hash {
			return path, nil
		}
	}
	if !current {
		return "", errors.New("cached PDF unavailable and Drive no longer serves this revision")
	}
	dir, err := tempDir(r.options.Spool)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "export.pdf")
	if err := r.google.Download(ctx, x.DriveFile, tmp); err != nil {
		return "", err
	}
	hash, _, err := pdfInfo(tmp, x.MD5)
	if err != nil {
		return "", err
	}
	if x.Hash != "" && x.Hash != hash {
		return "", errors.New("Drive content changed within the listed revision")
	}
	x.Hash = hash
	path := r.receiptPath(*x)
	if err := keepFile(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}
