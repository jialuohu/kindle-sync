// Package naming derives a portable Kindle filename from Zotero bibliographic metadata.
package naming

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"kindle-sync/internal/model"
)

var fourDigitYear = regexp.MustCompile(`^[1-9][0-9]{3}$`)

const nameWord = `[\p{L}][\p{L}\p{N}]*(?:[.\-][\p{L}\p{N}]+)*`

var singleWord = regexp.MustCompile(`^` + nameWord + `$`)

// Require the author to introduce a name followed by a description. A mere
// mention of an existing system is common in a paper comparing several tools.
var introduction = regexp.MustCompile(`(?i:\bwe\s+(?:present|introduce|propose|develop)\s+)(` + nameWord +
	`)(?:\s+\((` + nameWord + `)(?i:\s+for\s+short)\))?\s*,\s*(?i:a|an|our|which)\b`)

func AbstractArtifact(abstract string) string {
	var name string
	for _, m := range introduction.FindAllStringSubmatch(abstract, -1) {
		word := m[1]
		if m[2] != "" {
			word = m[2]
		}
		if strings.IndexFunc(word, unicode.IsUpper) < 0 {
			continue
		}
		if name != "" && !strings.EqualFold(word, name) {
			return ""
		}
		name = word
	}
	return name
}

func Filename(p model.Paper, artifactOverride string) (string, error) {
	author := slug(p.FirstAuthor, 48)
	if author == "" {
		author = "anon"
	}
	year := "nd"
	if fourDigitYear.MatchString(p.Year) {
		year = p.Year[2:]
	}
	label := slug(artifactOverride, 120)
	if artifactOverride != "" {
		if label == "" || strings.ContainsAny(artifactOverride, `/\`) || strings.IndexFunc(artifactOverride, unicode.IsControl) >= 0 {
			return "", errors.New("artifact must be a name containing letters or digits, without path separators or control characters")
		}
	} else {
		label = slug(artifactLabel(p), 120)
		if label == "" {
			label = slug(p.Key, 120)
		}
		if label == "" {
			return "", errors.New("paper has no usable title or key; specify an artifact name")
		}
	}
	return author + year + "-" + label + ".pdf", nil
}

func artifactLabel(p model.Paper) string {
	if p.Artifact != "" {
		return p.Artifact
	}
	if short := strings.TrimSpace(p.ShortTitle); singleWord.MatchString(short) {
		return short
	}
	title := strings.TrimSpace(p.Title)
	if i := strings.IndexAny(title, ":\uff1a"); i >= 0 {
		if prefix := strings.TrimSpace(title[:i]); singleWord.MatchString(prefix) {
			return prefix
		}
	}
	return title
}

// Keep letters, digits and their combining marks. Other characters separate
// words. Both limits leave space for the year, extension and any collision suffix.
func slug(s string, maxBytes int) string {
	var b strings.Builder
	separator := false
	for _, r := range norm.NFC.String(strings.ToLower(s)) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !(unicode.IsMark(r) && b.Len() > 0 && !separator) {
			separator = b.Len() > 0
			continue
		}
		room := maxBytes - b.Len()
		if separator {
			room--
		}
		if utf8.RuneLen(r) > room {
			break
		}
		if separator {
			b.WriteByte('-')
			separator = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
