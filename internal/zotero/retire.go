package zotero

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"kindle-sync/internal/model"
)

var notesDigest = regexp.MustCompile("^[0-9a-f]{64}$")

// RetireNotes moves one recorded attachment to Zotero trash after checking its
// replacement. The batch endpoint accepts a library version so edits to either
// attachment or the parent invalidate the write.
func (c *Client) RetireNotes(ctx context.Context, parent string, keep, stale model.Receipt) error {
	if !validKey.MatchString(parent) || keep.Child == stale.Child || keep.State != "archived" {
		return errors.New("invalid notes replacement")
	}
	for _, r := range []model.Receipt{keep, stale} {
		if r.Parent != parent || r.Child == parent || !validKey.MatchString(r.Child) || !notesDigest.MatchString(r.Hash) ||
			(r.MD5 != "" && !hashPattern.MatchString(r.MD5)) {
			return errors.New("invalid notes receipt")
		}
	}
	version, active, err := c.retirementSnapshot(ctx, parent, &keep, &stale)
	if err != nil || !active {
		return err
	}

	// A partial batch update leaves all fields other than deleted untouched.
	body := fmt.Sprintf(`[{"key":%q,"deleted":1}]`, stale.Child)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/items", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Zotero-API-Key", c.config.APIKey)
	req.Header.Set("Zotero-API-Version", "3")
	req.Header.Set("If-Unmodified-Since-Version", version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("moving old notes to Zotero trash: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpStatusError(resp.StatusCode)
	}
	// A multi-item update can return HTTP 200 with a failure for the item.
	var result struct {
		Failed map[string]json.RawMessage `json:"failed"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil || len(result.Failed) != 0 {
		return errors.New("Zotero did not confirm the notes update")
	}
	_, active, err = c.retirementSnapshot(ctx, parent, &keep, &stale)
	if err == nil && active {
		return errors.New("old notes are still active after the Zotero update")
	}
	return err
}

// Read exactly the three identities, including trashed items, in one library snapshot.
func (c *Client) retirementSnapshot(ctx context.Context, parent string, keep, stale *model.Receipt) (string, bool, error) {
	path := "/items?limit=3&includeTrashed=1&itemKey=" + parent + "," + keep.Child + "," + stale.Child
	resp, err := c.get(ctx, c.base+path)
	if err != nil {
		return "", false, err
	}
	var version string
	rows, err := readPage(resp, &version)
	if err != nil {
		return "", false, err
	}
	v, err := strconv.ParseUint(version, 10, 64)
	if err != nil || v == 0 {
		return "", false, errors.New("missing Zotero library version for notes update")
	}
	if total := resp.Header.Get("Total-Results"); (total != "" && total != strconv.Itoa(len(rows))) || strings.Contains(resp.Header.Get("Link"), `rel="next"`) {
		return "", false, errors.New("incomplete Zotero notes snapshot")
	}
	items := make(map[string]record, len(rows))
	for _, row := range rows {
		_, duplicate := items[row.Key]
		if duplicate || row.Key != row.Data.Key || (row.Key != parent && row.Key != keep.Child && row.Key != stale.Child) {
			return "", false, errors.New("unexpected item in Zotero notes snapshot")
		}
		items[row.Key] = row
	}
	p, ok := items[parent]
	if !ok {
		return "", false, model.ErrParentGone
	}
	if err := p.validateParent(parent); err != nil {
		return "", false, err
	}
	replacement, ok := items[keep.Child]
	if !ok {
		return "", false, errors.New("replacement notes are missing")
	}
	trashed, err := replacement.deleted()
	if err != nil {
		return "", false, err
	}
	if trashed {
		return "", false, errors.New("replacement notes are in the trash")
	}
	if err := c.verifyNotes(ctx, replacement, keep); err != nil {
		return "", false, fmt.Errorf("checking replacement notes: %w", err)
	}
	old, ok := items[stale.Child]
	if !ok {
		return version, false, nil
	}
	trashed, err = old.deleted()
	if err != nil || trashed {
		return version, false, err
	}
	if err := c.verifyNotes(ctx, old, stale); err != nil {
		return "", false, fmt.Errorf("checking old notes: %w", err)
	}
	return version, true, nil
}

func (c *Client) verifyNotes(ctx context.Context, row record, r *model.Receipt) error {
	d := row.Data
	if d.Parent != r.Parent || d.ItemType != "attachment" || d.LinkMode != "imported_file" ||
		d.ContentType != "application/pdf" || d.Filename != "Kindle Notes - "+r.Hash+".pdf" {
		return errors.New("attachment identity differs from the recorded notes")
	}
	// A partially created attachment may not yet have a stored file.
	if d.MD5 == "" && r.State == "pending" {
		return nil
	}
	if !hashPattern.MatchString(d.MD5) || (r.MD5 != "" && !strings.EqualFold(d.MD5, r.MD5)) {
		return errors.New("stored PDF checksum differs from the recorded notes")
	}
	if r.MD5 != "" {
		return nil
	}
	// Legacy receipts can lack a Drive MD5. Verify their recorded SHA-256
	// against the stored PDF before using the current MD5 for the readback.
	dir, err := os.MkdirTemp("", "kindle-notes-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "notes.pdf")
	if err := c.Download(ctx, model.Attachment{Key: r.Child, Name: d.Filename, MD5: d.MD5}, path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != r.Hash {
		return errors.New("stored PDF content differs from the recorded notes")
	}
	r.MD5 = d.MD5
	return nil
}
