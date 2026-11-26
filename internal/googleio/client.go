// Package googleio sends personal documents via Gmail and reads Scribe exports
// from one explicitly configured Drive folder.
package googleio

import (
	"context"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"sync"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Config struct {
	SecretsDir, Sender, KindleAddress, FolderID string
}

type Client struct {
	config       Config
	gmailService func() (*gmail.Service, error)
	driveService func() (*drive.Service, error)
}

var fileID = regexp.MustCompile(`^[0-9A-Za-z_-]+$`)

func New(ctx context.Context, c Config) (*Client, error) {
	if c.SecretsDir == "" {
		return nil, errors.New("set CODEX_SECRETS_DIR for Google credentials")
	}
	if c.FolderID == "root" || (c.FolderID != "" && !fileID.MatchString(c.FolderID)) {
		return nil, errors.New("invalid Google Drive folder ID")
	}
	return &Client{
		config: c,
		gmailService: sync.OnceValues(func() (*gmail.Service, error) {
			if !validAddress(c.Sender) || !validAddress(c.KindleAddress) {
				return nil, errors.New("configure a Gmail sender and Kindle destination")
			}
			client, err := authorizedClient(ctx, c, "gmail")
			if err != nil {
				return nil, err
			}
			return gmail.NewService(ctx, option.WithHTTPClient(client))
		}),
		driveService: sync.OnceValues(func() (*drive.Service, error) {
			if c.FolderID == "" {
				return nil, errors.New("configure the Scribe export folder ID")
			}
			client, err := authorizedClient(ctx, c, "drive")
			if err != nil {
				return nil, err
			}
			return drive.NewService(ctx, option.WithHTTPClient(client))
		}),
	}, nil
}

func validAddress(value string) bool {
	if strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func checkService(service string) error {
	if service != "gmail" && service != "drive" {
		return errors.New("select gmail or drive")
	}
	return nil
}
