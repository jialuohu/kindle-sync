package zotero

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
	"kindle-sync/internal/model"
)

// Find reads current collection membership without opening the sync database or loading attachment credentials.
func Find(ctx context.Context, libraryID, apiKey, collection, query string, aliases map[string]string) ([]model.Paper, error) {
	c, err := newClient(Config{LibraryID: libraryID, APIKey: apiKey})
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("find requires a nonempty title, artifact, key, or Zotero item link")
	}
	var itemKey string
	if validKey.MatchString(query) {
		itemKey = query
	} else if strings.Contains(query, "://") {
		itemKey, err = c.itemFromLink(ctx, query)
		if err != nil {
			return nil, err
		}
	}
	papers, err := c.Members(ctx, collection)
	if err != nil {
		return nil, err
	}
	needle := searchText(query)
	matches := []model.Paper{}
	for _, p := range papers {
		match := p.Key == itemKey
		if itemKey == "" {
			match = strings.EqualFold(query, p.Key) || strings.Contains(searchText(p.Title), needle) ||
				strings.Contains(searchText(p.ShortTitle), needle) || strings.Contains(searchText(p.Artifact), needle) ||
				strings.Contains(searchText(aliases[p.Key]), needle)
		}
		if match {
			if alias := aliases[p.Key]; alias != "" {
				p.Artifact = alias
			}
			matches = append(matches, p)
		}
	}
	return matches, nil
}

func searchText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(s))), " ")
}

var personalItemPath = regexp.MustCompile(`^/(users/[1-9][0-9]*|[^/]+)/(?:library/|collections/[A-Z0-9]{8}/)?items/([A-Z0-9]{8})(?:/(?:item-list|collection|library))?/?$`)

func (c *Client) itemFromLink(ctx context.Context, link string) (string, error) {
	u, err := url.Parse(link)
	if err != nil || u.User != nil {
		return "", errors.New("invalid Zotero item link")
	}
	if u.Scheme == "zotero" && u.Host == "select" {
		key, ok := strings.CutPrefix(u.Path, "/library/items/")
		if ok && validKey.MatchString(key) {
			return key, nil
		}
	}
	if u.Scheme != "https" || (u.Host != "www.zotero.org" && u.Host != "zotero.org" && u.Host != "api.zotero.org") {
		return "", errors.New("expected a personal Zotero item link")
	}
	parts := personalItemPath.FindStringSubmatch(u.Path)
	if parts == nil || parts[1] == "groups" {
		return "", errors.New("expected an item from the configured personal Zotero library")
	}
	if id, numeric := strings.CutPrefix(parts[1], "users/"); numeric {
		if id != c.config.LibraryID {
			return "", errors.New("Zotero link belongs to another library")
		}
	} else {
		// A web-library link can identify its owner by username. The same
		// eight-character item key in a different library is a different item.
		resp, err := c.get(ctx, "https://api.zotero.org/keys/current")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var account struct {
			UserID   uint64 `json:"userID"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&account); err != nil {
			return "", errors.New("could not read Zotero library identity")
		}
		if strconv.FormatUint(account.UserID, 10) != c.config.LibraryID || !strings.EqualFold(parts[1], account.Username) {
			return "", errors.New("Zotero link belongs to another library")
		}
	}
	return parts[2], nil
}
