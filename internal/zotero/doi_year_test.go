package zotero

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type registryTransport func(*http.Request) (*http.Response, error)

func (f registryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPublicationYearUsesOnlyTheExactPublicDOI(t *testing.T) {
	const doi = "10.5555/example"
	for _, tc := range []struct{ input, response, want string }{
		{doi, `{"message":{"DOI":"10.5555/EXAMPLE","published":{"date-parts":[[2024,5,1]]},"issued":{"date-parts":[[2023]]}}}`, "2024"},
		{"https://doi.org/10.5555/example", `{"message":{"DOI":"10.5555/example","issued":{"date-parts":[[2023]]}}}`, "2023"},
		{doi, `{"message":{"DOI":"10.5555/example","created":{"date-parts":[[2024]]}}}`, ""},
		{doi, `{"message":{"DOI":"10.5555/other","published":{"date-parts":[[2024]]}}}`, ""},
		{"https://example.invalid/private", "", ""},
	} {
		called := false
		c := &Client{config: Config{APIKey: "private-fixture"}, http: &http.Client{Transport: registryTransport(func(req *http.Request) (*http.Response, error) {
			called = true
			if req.Method != http.MethodGet || req.URL.Host != "api.crossref.org" || req.URL.Path != "/works/"+doi ||
				req.URL.RawQuery != "" || req.Body != nil || req.Header.Get("Zotero-API-Key") != "" || req.Header.Get("Authorization") != "" {
				t.Fatalf("unexpected public metadata request: %v", req.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.response)), Header: make(http.Header)}, nil
		})}}
		if got := c.PublicationYear(context.Background(), tc.input); got != tc.want || called != (tc.response != "") {
			t.Errorf("%s: got %q, requested=%t; want %q", tc.input, got, called, tc.want)
		}
	}
}
