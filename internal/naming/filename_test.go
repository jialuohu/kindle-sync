package naming

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"kindle-sync/internal/model"
)

func TestFilename(t *testing.T) {
	for _, tc := range []struct {
		name, author, year, title, short, abstract, override, want string
	}{
		{"named in title", "Lake", "2024", "PaperFinch: Indexing Library Catalogs", "", "", "", "lake24-paperfinch.pdf"},
		{"abbreviation in abstract", "Reed", "2026", "Searching Library Catalogs: An Index for Short Records", "Searching Library Catalogs", "We propose catalog indexing. We present QuillIndex (Quill for short), a catalog index.", "", "reed26-quill.pdf"},
		{"artifact override", "Reed", "2026", "Searching Library Catalogs", "Library Catalogs", "We propose XYZ, an accelerator.", "Quill", "reed26-quill.pdf"},
		{"author-supplied short title", "de Le\u00f3n", "2026", "Faster processing with Quill", "Quill", "", "", "de-le\u00f3n26-quill.pdf"},
		{"no invented name", "Ng", "1998", "A Survey: Processing with GPUs", "", "We compare CUDA, OpenCL, and Vulkan.", "", "ng98-a-survey-processing-with-gpus.pdf"},
		{"multiple names", "Han", "2020", "Universal Serving", "", "We introduce Alpha, a library. We present Beta, a scheduler.", "", "han20-universal-serving.pdf"},
		{"CJK characters and fullwidth slash", "\u738b", "2025", "Ignored", "", "", "\u5c71\uff0f\u6c34", "\u738b25-\u5c71-\u6c34.pdf"},
		{"canonical Unicode", "Le\u0301on", "2026", "Study", "", "", "", "l\u00e9on26-study.pdf"},
		{"missing metadata", "", "", "", "", "", "", "anonnd-abcd2345.pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := model.Paper{Key: "ABCD2345", FirstAuthor: tc.author, Year: tc.year, Title: tc.title, ShortTitle: tc.short, Artifact: AbstractArtifact(tc.abstract)}
			name, err := Filename(p, tc.override)
			if err != nil || name != tc.want {
				t.Fatalf("got %q, %v; want %q", name, err, tc.want)
			}
		})
	}
}

func TestFilenameSafeAndBounded(t *testing.T) {
	p := model.Paper{FirstAuthor: strings.Repeat("\u8303", 100), Year: "2026", Title: strings.Repeat("\U0001f58a\u4e00 /\u4eba", 200)}
	got, err := Filename(p, "")
	if err != nil || !utf8.ValidString(got) || filepath.Base(got) != got || len(got) > 175 {
		t.Fatalf("invalid portable filename %q, %v", got, err)
	}
	for _, bad := range []string{"", "  ", "../", "\\evil", "bad\nname"} {
		if _, err := Filename(model.Paper{}, bad); err == nil {
			t.Errorf("accepted unusable name %q", bad)
		}
	}
}
