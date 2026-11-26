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
	"strings"

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
	for _, f := range files {
		if f.ID == "" || f.Version == "" {
			return errors.New("Drive file is missing identity or revision")
		}
		key := revision{f.ID, f.Version}
		if seen := listedVersions[f.ID]; seen != "" && seen != f.Version {
			return errors.New("Drive file changed during listing; retry the scan")
		}
		current[key] = true
		listedVersions[f.ID] = f.Version
		if _, ok := r.receipts[key]; !ok {
			r.receipts[key] = model.Receipt{DriveFile: f, State: "pending"}
		}
	}
	var target revision
	if r.options.MatchFile != "" {
		if err != nil && r.options.MatchVersion == "" {
			return fmt.Errorf("cannot identify current Drive revision: %w", err)
		}
		target, err = r.matchTarget(current)
		if err != nil {
			return err
		}
	}
	tokenParents := map[string]string{}
	for _, j := range r.jobs {
		if j.Token != "" {
			tokenParents[j.Token] = j.Key
		}
	}
	work := make([]revision, 0)
	for k, rec := range r.receipts {
		if r.options.MatchFile != "" && k != target {
			continue
		}
		if rec.State == "archived" {
			if r.options.MatchFile != "" {
				r.report.Actions = append(r.report.Actions, "already archived: "+rec.ID)
			}
			continue
		}
		if listComplete && rec.Hash == "" && rec.Child == "" && newerVersion(listedVersions[k.id], rec.Version) {
			if r.options.MatchFile != "" {
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
			if r.options.MatchFile != "" {
				return errors.New(rec.Error)
			}
			continue
		}
		work = append(work, k)
	}
	sort.Slice(work, func(i, j int) bool {
		if work[i].id == work[j].id {
			return work[i].version < work[j].version
		}
		return work[i].id < work[j].id
	})
	for _, k := range work {
		if err := ctx.Err(); err != nil {
			return err
		}
		x := r.receipts[k]
		if x.Parent == "" {
			// Exporters may normalize spaces in the document title to underscores.
			name := strings.ReplaceAll(strings.ToUpper(x.Name), "_", " ")
			tokens := tokenPattern.FindAllString(name, -1)
			if len(tokens) == 1 {
				x.Parent = tokenParents[tokens[0]]
			}
		}
		if r.options.MatchFile != "" {
			if x.Parent != "" && x.Parent != r.options.MatchParent {
				return errors.New("export already identifies another parent")
			}
			if r.receipts[k].Parent == "" {
				if err := r.library.ValidateParent(ctx, r.options.MatchParent); err != nil {
					return fmt.Errorf("cannot match export to Zotero parent: %w", err)
				}
			}
			x.Parent = r.options.MatchParent
		}
		if x.State == "needs_match" && x.Parent == "" && digestPattern.MatchString(x.Hash) {
			if info, err := os.Stat(r.receiptPath(x)); err == nil && info.Mode().IsRegular() {
				continue
			}
		}
		if r.options.DryRun {
			action := "would hold for matching: "
			if x.Parent != "" {
				action = "would reconcile export for " + x.Parent + ": "
			}
			r.report.Actions = append(r.report.Actions, action+x.ID)
			continue
		}
		if err := r.receive(ctx, x, current[k]); err != nil {
			return err
		}
	}
	return nil
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
		if key.id != r.options.MatchFile {
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

func (r *round) receive(ctx context.Context, x model.Receipt, current bool) error {
	path, err := r.materialize(ctx, &x, current)
	if err != nil {
		return r.receiptProblem(x, err)
	}
	if x.Parent == "" {
		x.State, x.Error = "needs_match", "no uniquely recognized delivery code"
		r.report.Actions = append(r.report.Actions, "needs match: "+x.ID)
		return r.saveReceipt(x)
	}
	x.State, x.Error = "pending", ""
	// Save the identity of the parent and bytes before a potentially committing remote write.
	if err := r.saveReceipt(x); err != nil {
		return err
	}
	for _, old := range r.receipts {
		if old.Parent == x.Parent && old.Hash == x.Hash && old.Child != "" {
			x.Child = old.Child
			if old.State == "archived" {
				return r.archived(x, path)
			}
		}
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
	return r.archived(x, path)
}

func (r *round) receiptProblem(x model.Receipt, err error) error {
	x.Error = err.Error()
	r.report.Problems = append(r.report.Problems, x.ID+": "+x.Error)
	return r.saveReceipt(x)
}

func (r *round) archived(x model.Receipt, path string) error {
	x.State, x.Error = "archived", ""
	if err := r.saveReceipt(x); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Dir(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	r.report.Actions = append(r.report.Actions, "archived "+x.ID+" under "+x.Parent+" as "+x.Child)
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
