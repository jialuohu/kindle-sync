// Command kindle-sync runs the server-side sync job and its agent-facing CLI.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"kindle-sync/internal/engine"
	"kindle-sync/internal/googleio"
	"kindle-sync/internal/model"
	"kindle-sync/internal/state"
	"kindle-sync/internal/zotero"
)

type config struct {
	LibraryID    string `json:"library_id"`
	Collection   string `json:"collection_key"`
	Sender       string `json:"sender_email"`
	Kindle       string `json:"kindle_email"`
	Folder       string `json:"drive_folder_id"`
	StateDir     string `json:"state_dir"`
	WebDAV       string `json:"webdav_url"`
	HelperPath   string `json:"helper_path"`
	HelperPython string `json:"helper_python"`
	MailLimit    int64  `json:"email_limit_bytes"`
}

const usage = `kindle-sync [--config PATH] COMMAND [--json]
  auth gmail|drive                  Authorize the server through a loopback browser callback
  sync [--dry-run]                  Discover papers, submit mail and archive receipts
  sync --backfill COLLECTION        Include historical members of this collection
  sync --item ITEM --attachment KEY Choose a source PDF explicitly
  resolve ITEM received|retry       Resolve an uncertain send after checking the device
  resolve ITEM rejected GMAIL_ID    Queue a retry after Amazon rejected that submission
  status                            Show the local queue and Drive receipts
  match DRIVE_FILE_ID ITEM          Archive a returned PDF under the specified paper

Config defaults to KINDLE_SYNC_CONFIG or /etc/kindle-sync/config.json.
Secrets are loaded from CODEX_SECRETS_DIR. Agent command results are JSON.
`

var zotKey = regexp.MustCompile(`^[A-Z0-9]{8}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}
func readConfig(p string) (config, error) {
	var c config
	f, err := os.Open(p)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("unexpected trailing config data")
	}
	if c.MailLimit == 0 {
		c.MailLimit = 25_000_000
	}
	if c.MailLimit < 1 {
		return c, errors.New("email_limit_bytes must be positive")
	}
	if c.MailLimit > googleio.MaxPDFUploadBytes {
		c.MailLimit = googleio.MaxPDFUploadBytes
	}
	return c, nil
}

func configArgs(args []string) (string, []string, error) {
	p := os.Getenv("KINDLE_SYNC_CONFIG")
	if p == "" {
		p = "/etc/kindle-sync/config.json"
	}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config":
			i++
			if i == len(args) {
				return "", nil, errors.New("--config needs a path")
			}
			p = args[i]
		case strings.HasPrefix(args[i], "--config="):
			p = strings.TrimPrefix(args[i], "--config=")
		case args[i] == "--json": // Output is always JSON.
		default:
			rest = append(rest, args[i])
		}
	}
	return p, rest, nil
}

func parseOptions(args []string, c config) (model.Options, error) {
	o := model.Options{Root: c.Collection, Spool: filepath.Join(c.StateDir, "spool"), MaxEmailBytes: c.MailLimit, SkipReturns: c.Folder == ""}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	if args[0] == "sync" {
		f.BoolVar(&o.DryRun, "dry-run", false, "")
		f.StringVar(&o.Item, "item", "", "")
		f.StringVar(&o.Attachment, "attachment", "", "")
		f.StringVar(&o.Backfill, "backfill", "", "")
	}
	if args[0] == "match" {
		f.StringVar(&o.MatchVersion, "version", "", "")
	}
	if err := f.Parse(args[1:]); err != nil {
		return o, err
	}
	if args[0] == "match" {
		if f.NArg() != 2 || f.Arg(0) == "" || !zotKey.MatchString(f.Arg(1)) {
			return o, errors.New("match requires DRIVE_FILE_ID and Zotero paper key")
		}
		o.MatchFile, o.MatchParent = f.Arg(0), f.Arg(1)
	} else if f.NArg() != 0 {
		return o, errors.New("unexpected positional argument")
	}
	if (o.Item == "") != (o.Attachment == "") || (o.Item != "" && o.Backfill != "") {
		return o, errors.New("use --item and --attachment together, separately from --backfill")
	}
	for _, k := range []string{o.Item, o.Attachment, o.Backfill} {
		if k != "" && !zotKey.MatchString(k) {
			return o, errors.New("expected an eight-character Zotero key")
		}
	}
	return o, nil
}

func (c config) identity() (string, error) {
	if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(c.LibraryID) || !zotKey.MatchString(c.Collection) {
		return "", errors.New("set library_id and collection_key for the personal library")
	}
	for _, v := range []string{c.Sender, c.Kindle} {
		a, e := mail.ParseAddress(v)
		if e != nil || a.Address != v {
			return "", errors.New("set valid sender_email and kindle_email in the configuration")
		}
	}

	sum := sha256.Sum256([]byte(strings.Join([]string{c.LibraryID, c.Collection, strings.ToLower(c.Kindle)}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

func readSecret(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("cannot read %s in CODEX_SECRETS_DIR", name)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("empty %s", name)
	}
	return v, nil
}

func run(ctx context.Context, args []string, out io.Writer) error {
	p, args, err := configArgs(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err = io.WriteString(out, usage)
		return err
	}
	switch args[0] {
	case "auth", "sync", "status", "match", "resolve":
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	c, err := readConfig(p)
	if err != nil {
		return err
	}
	secretDir := os.Getenv("CODEX_SECRETS_DIR")
	if args[0] != "status" && args[0] != "resolve" && !filepath.IsAbs(secretDir) {
		return errors.New("set CODEX_SECRETS_DIR to an absolute directory")
	}
	gc := googleio.Config{SecretsDir: secretDir, Sender: c.Sender, KindleAddress: c.Kindle, FolderID: c.Folder}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if args[0] == "auth" {
		if len(args) != 2 || (args[1] != "gmail" && args[1] != "drive") {
			return errors.New("auth requires gmail or drive")
		}
		if err := googleio.Authorize(ctx, gc, args[1]); err != nil {
			return err
		}
		return enc.Encode(map[string]string{"authorized": args[1]})
	}
	if args[0] == "resolve" {
		if len(args) < 3 || len(args) > 4 || !zotKey.MatchString(args[1]) || (args[2] == "rejected") != (len(args) == 4) {
			return errors.New("use resolve ITEM received|retry or resolve ITEM rejected GMAIL_ID")
		}
		var gmailID string
		if len(args) == 4 {
			gmailID = args[3]
		}
		id, err := c.identity()
		if err != nil {
			return err
		}
		s, err := state.Open(c.StateDir, id, true)
		if err != nil {
			return err
		}
		defer s.Close()
		job, err := engine.Resolve(s, args[1], args[2], gmailID)
		if err != nil {
			return err
		}
		return enc.Encode(job)
	}
	o, err := parseOptions(args, c)
	if err != nil {
		return err
	}
	id, err := c.identity()
	if err != nil {
		return err
	}
	s, err := state.Open(c.StateDir, id, args[0] != "status" && !o.DryRun)
	if err != nil {
		return err
	}
	defer s.Close()
	if args[0] == "match" && c.Folder == "" {
		return errors.New("set drive_folder_id before matching an export")
	}
	if err := s.BindFolder(c.Folder, false); err != nil {
		return err
	}
	if args[0] == "status" {
		j, err := s.Jobs()
		if err != nil {
			return err
		}
		r, err := s.Receipts()
		if err != nil {
			return err
		}
		init, err := s.Initialized()
		if err != nil {
			return err
		}
		return enc.Encode(map[string]any{"initialized": init, "jobs": j, "receipts": r})
	}
	zc := zotero.Config{LibraryID: c.LibraryID, WebDAVURL: c.WebDAV, HelperPath: c.HelperPath, HelperPython: c.HelperPython, LockDir: filepath.Join(c.StateDir, "helper-locks")}
	for _, secret := range []struct {
		name   string
		target *string
	}{{"zotero-api-key", &zc.APIKey}, {"koofr-username", &zc.WebDAVUsername}, {"koofr-password", &zc.WebDAVPassword}} {
		*secret.target, err = readSecret(secretDir, secret.name)
		if err != nil {
			return err
		}
	}
	z, err := zotero.New(zc)
	if err != nil {
		return err
	}
	g, err := googleio.New(ctx, gc)
	if err != nil {
		return err
	}
	var google model.Google = g
	if !o.DryRun && c.Folder != "" {
		google = &folderBinding{Google: g, store: s, folder: c.Folder}
	}
	report, runErr := engine.Run(ctx, s, z, google, o)
	if err = enc.Encode(report); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	if len(report.Problems) > 0 {
		return errors.New("some items need attention; see problems in the JSON report")
	}
	return nil
}

// Claim a newly configured folder only after Google verified it and listed
// its files. A bad folder ID or expired token must leave the pilot recoverable.
type folderBinding struct {
	model.Google
	store  *state.Store
	folder string
}

func (g *folderBinding) Files(ctx context.Context) ([]model.DriveFile, error) {
	files, err := g.Google.Files(ctx)
	if err != nil {
		return nil, err
	}
	if err = g.store.BindFolder(g.folder, true); err != nil {
		return nil, err
	}
	return files, nil
}
