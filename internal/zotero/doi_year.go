package zotero

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PublicationYear looks up a single public DOI. Zotero credentials are used
// only by get(); this request goes directly to Crossref without them.
func (c *Client) PublicationYear(ctx context.Context, doi string) string {
	doi = normalizeDOI(doi)
	if doi == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.crossref.org/works/"+url.PathEscape(doi), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindle-sync (bibliographic year lookup)")
	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	type date struct {
		Parts [][]int `json:"date-parts"`
	}
	var result struct {
		Message struct {
			DOI       string `json:"DOI"`
			Published date   `json:"published"`
			Issued    date   `json:"issued"`
		} `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil || !strings.EqualFold(doi, result.Message.DOI) {
		return ""
	}
	for _, d := range []date{result.Message.Published, result.Message.Issued} {
		if len(d.Parts) == 1 && len(d.Parts[0]) != 0 {
			y := d.Parts[0][0]
			if y >= 1000 && y <= 9999 {
				return strconv.Itoa(y)
			}
		}
	}
	return ""
}
