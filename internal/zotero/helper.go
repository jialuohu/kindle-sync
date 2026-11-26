package zotero

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"kindle-sync/internal/model"
)

type receipt struct {
	Status   string `json:"status"`
	Error    string `json:"error"`
	Backend  string `json:"backend"`
	Parent   string `json:"parent_key"`
	Child    string `json:"attachment_key"`
	Basename string `json:"basename"`
	MD5      string `json:"md5"`
}

var errorCode = regexp.MustCompile("^[a-z][a-z0-9_]{0,79}$")

func (c *Client) helper(ctx context.Context, args ...string) (receipt, error) {
	cfg := c.config
	cmd := exec.CommandContext(ctx, cfg.HelperPython, append([]string{"-I", "-B", cfg.HelperPath}, args...)...)
	cmd.Env = []string{
		"ZOTERO_LIBRARY_TYPE=user",
		"ZOTERO_LIBRARY_ID=" + cfg.LibraryID,
		"ZOTERO_API_KEY=" + cfg.APIKey,
		"ZOTERO_WEBDAV_URL=" + cfg.WebDAVURL,
		"ZOTERO_WEBDAV_USERNAME=" + cfg.WebDAVUsername,
		"ZOTERO_WEBDAV_PASSWORD=" + cfg.WebDAVPassword,
		"KINDLE_SYNC_LOCK_DIR=" + cfg.LockDir,
		"CODEX_PAPER_INTAKE_REEXEC=1",
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "HTTPS_PROXY", "NO_PROXY", "https_proxy", "no_proxy"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	data, runErr := cmd.Output()
	if ctx.Err() != nil {
		return receipt{}, ctx.Err()
	}
	var result receipt
	if json.Unmarshal(data, &result) != nil {
		return receipt{}, errors.New("attachment helper did not return a JSON receipt; check interpreter and helper installation")
	}
	if runErr != nil || result.Error != "" {
		msg := "attachment helper failed"
		if errorCode.MatchString(result.Error) {
			msg += ": " + result.Error
		}
		return result, errors.New(msg)
	}
	return result, nil
}

func (c *Client) Download(ctx context.Context, a model.Attachment, dst string) error {
	if !validKey.MatchString(a.Key) || a.Name == "" || a.Name == "." || a.Name == ".." || filepath.Base(a.Name) != a.Name || strings.Contains(a.Name, "\\") {
		return errors.New("invalid Zotero attachment")
	}
	path, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	args := []string{"fetch", "--attachment-key", a.Key, "--filename", a.Name, "--output", path}
	if a.MD5 != "" {
		if !hashPattern.MatchString(a.MD5) {
			return errors.New("invalid Zotero attachment checksum")
		}
		args = append(args, "--md5", a.MD5)
	}
	r, err := c.helper(ctx, args...)
	if err != nil {
		if r.Error == "source_pdf_too_large" || r.Error == "webdav_archive_too_large" {
			return fmt.Errorf("%w: %s", model.ErrTooLarge, r.Error)
		}
		return err
	}
	if r.Status != "downloaded" || r.Child != a.Key || !hashPattern.MatchString(r.MD5) || (a.MD5 != "" && !strings.EqualFold(r.MD5, a.MD5)) {
		return errors.New("attachment helper returned inconsistent download receipt")
	}
	return nil
}

func (c *Client) Archive(ctx context.Context, parent, path, child string) (string, error) {
	if !validKey.MatchString(parent) || (child != "" && !validKey.MatchString(child)) {
		return "", errors.New("invalid Zotero archive key")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	args := []string{"attach", "--parent-key", parent, "--file", abs}
	if child != "" {
		args = append(args, "--attachment-key", child)
	}
	r, err := c.helper(ctx, args...)
	if r.Parent != parent || !validKey.MatchString(r.Child) || (child != "" && r.Child != child) {
		if err != nil {
			return child, err
		}
		return child, errors.New("archive receipt has unexpected Zotero keys")
	}
	if err != nil {
		return r.Child, err
	}
	if r.Backend != "webdav" || r.Basename != filepath.Base(abs) || !hashPattern.MatchString(r.MD5) {
		return r.Child, errors.New("incomplete WebDAV readback receipt")
	}
	switch r.Status {
	case "created", "repaired", "unchanged":
		return r.Child, nil
	}
	return r.Child, errors.New("unknown attachment helper outcome")
}
