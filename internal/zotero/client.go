// Package zotero reads personal libraries and works with their stored WebDAV attachments.
package zotero

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"kindle-sync/internal/model"
	"kindle-sync/internal/naming"
)

type Config struct {
	LibraryID, APIKey, WebDAVURL, WebDAVUsername, WebDAVPassword string
	HelperPython, HelperPath, LockDir                            string
}

type Client struct {
	config Config
	base   string
	http   *http.Client
}

var validKey = regexp.MustCompile("^[A-Z0-9]{8}$")
var managedName = regexp.MustCompile("(?i)^Kindle Notes - [0-9a-f]{64}\\.pdf$")
var hashPattern = regexp.MustCompile("^[[:xdigit:]]{32}$")

func New(c Config) (*Client, error) {
	if id, err := strconv.ParseUint(c.LibraryID, 10, 64); err != nil || id == 0 || c.APIKey == "" {
		return nil, errors.New("personal Zotero library ID and API key required")
	}
	u, err := url.Parse(c.WebDAVURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.WebDAVUsername == "" || c.WebDAVPassword == "" {
		return nil, errors.New("an HTTPS WebDAV directory and credentials are required")
	}
	if !filepath.IsAbs(c.HelperPython) || !filepath.IsAbs(c.HelperPath) || !filepath.IsAbs(c.LockDir) {
		return nil, errors.New("absolute helper interpreter, script, and lock directory paths required")
	}
	transport := &http.Client{
		Timeout:       45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{config: c, base: "https://api.zotero.org/users/" + c.LibraryID, http: transport}, nil
}

type record struct {
	Key  string `json:"key"`
	Meta struct {
		ParsedDate string `json:"parsedDate"`
	} `json:"meta"`
	Data struct {
		Key        string          `json:"key"`
		Deleted    json.RawMessage `json:"deleted"`
		Title      string          `json:"title"`
		ShortTitle string          `json:"shortTitle"`
		Date       string          `json:"date"`
		Abstract   string          `json:"abstractNote"`
		Creators   []struct {
			Type     string `json:"creatorType"`
			LastName string `json:"lastName"`
			Name     string `json:"name"` // Zotero's single-field (organization) creator.
		} `json:"creators"`
		ItemType    string `json:"itemType"`
		LinkMode    string `json:"linkMode"`
		ContentType string `json:"contentType"`
		Filename    string `json:"filename"`
		MD5         string `json:"md5"`
		Parent      string `json:"parentItem"`
	} `json:"data"`
}

var publicationYear = regexp.MustCompile(`\b[1-9][0-9]{3}\b`)

func (r record) paper() model.Paper {
	p := model.Paper{
		Key: r.Key, Title: r.Data.Title, ShortTitle: r.Data.ShortTitle,
		Artifact: naming.AbstractArtifact(r.Data.Abstract),
	}
	for _, c := range r.Data.Creators {
		if c.Type == "author" {
			p.FirstAuthor = strings.TrimSpace(c.LastName)
			if p.FirstAuthor == "" {
				p.FirstAuthor = strings.TrimSpace(c.Name)
			}
			break
		}
	}
	p.Year = publicationYear.FindString(r.Meta.ParsedDate)
	if p.Year == "" {
		// Older data may only expose a display date. An ambiguous date range
		// supplies no single publication year. Never use dateAdded or dateModified.
		if years := publicationYear.FindAllString(r.Data.Date, 2); len(years) == 1 {
			p.Year = years[0]
		}
	}
	return p
}

func (c *Client) get(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Zotero-API-Key", c.config.APIKey)
	req.Header.Set("Zotero-API-Version", "3")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Zotero request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("Zotero returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) ValidateParent(ctx context.Context, key string) error {
	if !validKey.MatchString(key) {
		return errors.New("invalid Zotero parent key")
	}
	resp, err := c.get(ctx, c.base+"/items/"+key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var row record
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&row); err != nil {
		return errors.New("invalid Zotero parent response")
	}
	if row.Key != key || row.Data.Key != key || row.Data.Parent != "" {
		return errors.New("Zotero parent identity mismatch")
	}
	switch row.Data.ItemType {
	case "attachment", "annotation", "note", "":
		return errors.New("expected a regular Zotero parent item")
	}
	switch string(row.Data.Deleted) {
	case "", "false", "0":
		return nil
	default:
		return errors.New("Zotero parent item is deleted or unreadable")
	}
}

// Keep all pages of a tree scan at one library revision. A concurrent edit
// invalidates the scan and the scheduler retries it before recording membership.
func (c *Client) records(ctx context.Context, path string, version *string) ([]record, error) {
	const pageSize = 100
	var records []record
	for start := 0; ; {
		addr := fmt.Sprintf("%s/%s?limit=%d&start=%d", c.base, path, pageSize, start)
		resp, err := c.get(ctx, addr)
		if err != nil {
			return nil, err
		}
		page, err := readPage(resp, version)
		if err != nil {
			return nil, err
		}
		records = append(records, page...)
		start += len(page)
		total, err := strconv.Atoi(resp.Header.Get("Total-Results"))
		if err == nil {
			if start >= total {
				break
			}
		} else if len(page) < pageSize && !strings.Contains(resp.Header.Get("Link"), `rel="next"`) {
			break
		}
		if len(page) == 0 {
			return nil, errors.New("empty Zotero continuation page")
		}
	}
	return records, nil
}

func readPage(resp *http.Response, version *string) ([]record, error) {
	defer resp.Body.Close()
	current := resp.Header.Get("Last-Modified-Version")
	if *version != "" && current != *version {
		return nil, errors.New("Zotero library changed during scan")
	}
	*version = current
	const limit = 16 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, errors.New("Zotero page too large")
	}
	var page []record
	if err = json.Unmarshal(b, &page); err != nil || page == nil {
		return nil, errors.New("invalid Zotero result page")
	}
	for _, row := range page {
		if !validKey.MatchString(row.Key) {
			return nil, errors.New("invalid record key in Zotero response")
		}
	}
	return page, nil
}

func (c *Client) Members(ctx context.Context, root string) ([]model.Paper, error) {
	if !validKey.MatchString(root) {
		return nil, errors.New("invalid Zotero collection key")
	}
	pending := []string{root}
	visited := map[string]bool{root: true}
	items := map[string]model.Paper{}
	var revision string
	for len(pending) > 0 {
		key := pending[0]
		pending = pending[1:]
		prefix := "collections/" + key
		rows, err := c.records(ctx, prefix+"/items/top", &revision)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			switch row.Data.ItemType {
			case "attachment", "annotation", "note":
				continue
			case "":
				return nil, errors.New("missing Zotero item type")
			}
			items[row.Key] = row.paper()
		}
		children, err := c.records(ctx, prefix+"/collections", &revision)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if !visited[child.Key] {
				visited[child.Key] = true
				pending = append(pending, child.Key)
			}
		}
	}
	result := make([]model.Paper, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func (c *Client) Sources(ctx context.Context, parent string) ([]model.Attachment, error) {
	if !validKey.MatchString(parent) {
		return nil, errors.New("invalid Zotero parent key")
	}
	var revision string
	rows, err := c.records(ctx, "items/"+parent+"/children", &revision)
	if err != nil {
		return nil, err
	}
	var out []model.Attachment
	for _, r := range rows {
		d := r.Data
		if d.Parent != parent || d.ItemType != "attachment" || (d.LinkMode != "imported_file" && d.LinkMode != "imported_url") || managedName.MatchString(d.Filename) || managedName.MatchString(d.Title) {
			continue
		}
		if d.Filename == "" || filepath.Base(d.Filename) != d.Filename || strings.Contains(d.Filename, "\\") {
			continue
		}
		if !(strings.EqualFold(d.ContentType, "application/pdf") || (d.ContentType == "" && strings.EqualFold(filepath.Ext(d.Filename), ".pdf"))) {
			continue
		}
		out = append(out, model.Attachment{Key: r.Key, Name: d.Filename, MD5: d.MD5})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
