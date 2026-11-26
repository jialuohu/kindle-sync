package googleio

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"kindle-sync/internal/model"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const maxReturnBytes = 200 << 20 // Matches the PDF bound enforced by the archiver.
const driveMetadata googleapi.Field = "id,name,version,md5Checksum,parents,trashed,mimeType,size"

func (c *Client) Files(ctx context.Context) ([]model.DriveFile, error) {
	svc, err := c.driveService()
	if err != nil {
		return nil, err
	}
	folder, err := svc.Files.Get(c.config.FolderID).Fields("mimeType,trashed").Context(ctx).Do()
	if err != nil {
		return nil, driveError("check export folder", err)
	}
	if folder.Trashed || folder.MimeType != "application/vnd.google-apps.folder" {
		return nil, errors.New("configured Scribe folder is unavailable")
	}
	q := fmt.Sprintf("'%s' in parents and trashed = false and mimeType = 'application/pdf'", c.config.FolderID)
	call := svc.Files.List().Spaces("drive").Q(q).PageSize(1000).
		Fields("nextPageToken,incompleteSearch,files(id,name,version,md5Checksum,createdTime)")
	var files []model.DriveFile
	err = call.Pages(ctx, func(page *drive.FileList) error {
		if page.IncompleteSearch {
			return errors.New("Google Drive search was incomplete")
		}
		for _, f := range page.Files {
			if f.Id == "" || f.Version <= 0 {
				return errors.New("Drive returned a PDF without a stable identity or version")
			}
			files = append(files, model.DriveFile{ID: f.Id, Name: f.Name, Version: strconv.FormatInt(f.Version, 10), MD5: f.Md5Checksum, CreatedTime: f.CreatedTime})
		}
		return nil
	})
	if err != nil {
		return nil, driveError("list exports", err)
	}
	return files, nil
}

func driveError(action string, err error) error {
	var ae *googleapi.Error
	if errors.As(err, &ae) {
		return fmt.Errorf("Drive %s: HTTP %d", action, ae.Code)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Drive %s canceled: %w", action, err)
	}
	return fmt.Errorf("Drive %s failed; check connection, folder access and authorization", action)
}

func (c *Client) Download(ctx context.Context, f model.DriveFile, dst string) error {
	svc, err := c.driveService()
	if err != nil {
		return err
	}
	if !fileID.MatchString(f.ID) {
		return errors.New("invalid Drive file ID")
	}
	// A PDF can be replaced, renamed, or moved between the scan and download.
	// Recheck version and folder membership before reading and before publishing.
	check := func() (*drive.File, error) {
		meta, err := svc.Files.Get(f.ID).Fields(driveMetadata).Context(ctx).Do()
		if err != nil {
			return nil, driveError("check export metadata", err)
		}
		if meta.Trashed || meta.MimeType != "application/pdf" || !slices.Contains(meta.Parents, c.config.FolderID) ||
			meta.Id != f.ID || meta.Name != f.Name || strconv.FormatInt(meta.Version, 10) != f.Version ||
			(f.MD5 != "" && !strings.EqualFold(f.MD5, meta.Md5Checksum)) {
			return nil, errors.New("Drive export changed or left its configured folder; rescan")
		}
		if meta.Size <= 0 || meta.Size > maxReturnBytes {
			return nil, errors.New("Drive PDF is empty or exceeds the archive size limit")
		}
		return meta, nil
	}
	meta, err := check()
	if err != nil {
		return err
	}
	res, err := svc.Files.Get(f.ID).Context(ctx).Download()
	if err != nil {
		return driveError("download export", err)
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".drive-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	digest := md5.New()
	n, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(res.Body, meta.Size+1))
	if err != nil || n != meta.Size || (meta.Md5Checksum != "" && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), meta.Md5Checksum)) {
		return errors.New("Drive export transfer failed size or checksum verification")
	}
	if _, err := check(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
