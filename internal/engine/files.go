package engine

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const webLimit = 200_000_000

var tokenPattern = regexp.MustCompile(`(?i)\bKRS-[A-Z2-7]{16}\b`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Preserve the filename of deliveries created by token-based versions on retry.
func legacyFilename(token, title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:"*?<>|`, r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	// Leave room for the identifying prefix and extension in a portable filename.
	for len(title) > 170 {
		_, n := utf8.DecodeLastRuneInString(title)
		title = title[:len(title)-n]
	}
	return token + " " + strings.TrimSpace(title) + ".pdf"
}

func pdfInfo(path, expectedMD5 string) (hash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", 0, err
	}
	if !bytes.Contains(head[:n], []byte("%PDF-")) {
		return "", 0, errors.New("download is not a PDF")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	h, legacy := sha256.New(), md5.New()
	size, err = io.Copy(io.MultiWriter(h, legacy), f)
	if err != nil {
		return "", 0, err
	}
	if expectedMD5 != "" && !strings.EqualFold(expectedMD5, hex.EncodeToString(legacy.Sum(nil))) {
		return "", 0, errors.New("download checksum differs from file metadata")
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func tempDir(root string) (string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, ".sync-")
}

func keepFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	if err := os.Chmod(src, 0600); err != nil {
		return err
	}
	return os.Rename(src, dest)
}
