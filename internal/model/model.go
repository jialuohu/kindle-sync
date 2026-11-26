// Package model defines the data exchanged by the sync workflow and adapters.
package model

import (
	"context"
	"errors"
)

var ErrUncertain = errors.New("mail submission outcome is unknown")
var ErrTooLarge = errors.New("PDF exceeds the supported size limit")
var ErrSendRejected = errors.New("mail submission was rejected")
var ErrParentGone = errors.New("Zotero parent item is in the trash or no longer exists")

type Paper struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	FirstAuthor string `json:"first_author,omitempty"`
	Year        string `json:"year,omitempty"`
	ShortTitle  string `json:"short_title,omitempty"`
	Artifact    string `json:"artifact,omitempty"` // Name explicitly introduced in the abstract.
}
type Attachment struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	MD5  string `json:"md5,omitempty"`
}
type DriveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	MD5          string `json:"md5,omitempty"`
	CreatedTime  string `json:"created_time,omitempty"`
	ModifiedTime string `json:"modified_time,omitempty"`
}
type Zotero interface {
	ValidateParent(context.Context, string) error
	// Members returns all regular papers beneath a collection (inclusive).
	Members(context.Context, string) ([]Paper, error)
	Sources(context.Context, string) ([]Attachment, error)
	Download(context.Context, Attachment, string) error                        // destination file path
	Archive(context.Context, string, string, string) (string, error)           // parent, file path, previous child key if known; return child key even for partial failures
	RetireNotes(ctx context.Context, parent string, keep, stale Receipt) error // verify both identities before moving stale to Zotero trash
}
type Google interface {
	Send(context.Context, string, string) (string, error) // complete file path, attachment filename; Gmail message ID
	Files(context.Context) ([]DriveFile, error)
	Download(context.Context, DriveFile, string) error
}
type Job struct {
	Paper
	State            string       `json:"state"`
	Token            string       `json:"token,omitempty"`
	OutboundName     string       `json:"outbound_name,omitempty"`
	NamedAt          string       `json:"named_at,omitempty"`
	Source           string       `json:"source,omitempty"` // attachment key
	SourceHash       string       `json:"source_hash,omitempty"`
	Candidates       []Attachment `json:"candidates,omitempty"`
	GmailID          string       `json:"gmail_id,omitempty"`
	RejectedGmailIDs []string     `json:"rejected_gmail_ids,omitempty"`
	ManualFile       string       `json:"manual_file,omitempty"`
	Error            string       `json:"error,omitempty"`
}
type Receipt struct {
	DriveFile
	Parent         string `json:"parent,omitempty"`
	State          string `json:"state"`
	Hash           string `json:"sha256,omitempty"`
	Child          string `json:"child,omitempty"`
	Error          string `json:"error,omitempty"`
	ObservedBefore string `json:"observed_before,omitempty"` // upper bound for an archived export with no Drive timestamp
}
type Store interface {
	Initialized() (bool, error)
	Seed([]Paper) error // atomically insert skipped baseline jobs + initialized marker
	Jobs() ([]Job, error)
	PutJob(Job) error
	Receipts() ([]Receipt, error)
	PutReceipt(Receipt) error // identity = drive file ID + version
}
type Options struct {
	Root, Backfill, Item, Attachment, ReturnFile, MatchParent, MatchVersion, Spool string
	Direction, Artifact                                                            string
	ArtifactNames                                                                  map[string]string
	MaxEmailBytes                                                                  int64
	DryRun, SkipReturns                                                            bool
}

// SyncDirection keeps ordinary sync bidirectional and selects a single leg for targeted work.
func (o Options) SyncDirection() string {
	if o.Direction != "" {
		return o.Direction
	}
	if o.ReturnFile != "" {
		return "receive"
	}
	if o.Item != "" || o.Backfill != "" {
		return "send"
	}
	return "both"
}

type Report struct {
	Actions  []string `json:"actions"`
	Problems []string `json:"problems"`
}
