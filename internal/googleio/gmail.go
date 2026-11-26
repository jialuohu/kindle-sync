package googleio

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"strings"
	"time"
	"unicode"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"kindle-sync/internal/model"
)

// MaxPDFUploadBytes reserves 8 KiB for message headers and MIME delimiters
// under the Gmail users.messages.send discovery mediaUpload limit of 35 MiB.
// Each 57 PDF bytes occupy up to 78 bytes after base64 and CRLF folding.
const MaxPDFUploadBytes = ((35 << 20) - 8192) / 78 * 57

// Upload an RFC 5322 message with one real application/pdf attachment. Using
// the media endpoint avoids JSON base64-wrapping the already encoded MIME.
func (c *Client) Send(ctx context.Context, path, filename string) (string, error) {
	service, err := c.gmailService()
	if err != nil {
		return "", fmt.Errorf("%w: %v", model.ErrSendRejected, err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %v", model.ErrSendRejected, err)
	}
	pdf, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: cannot open outgoing PDF", model.ErrSendRejected)
	}
	defer pdf.Close()
	msg, err := os.CreateTemp("", "kindle-sync-mail-*.eml")
	if err != nil {
		return "", fmt.Errorf("%w: cannot stage MIME message", model.ErrSendRejected)
	}
	defer os.Remove(msg.Name())
	defer msg.Close()
	if err := writeMessage(msg, c.config.Sender, c.config.KindleAddress, filename, pdf); err != nil {
		return "", fmt.Errorf("%w: %v", model.ErrSendRejected, err)
	}
	if _, err := msg.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("%w: cannot read staged message", model.ErrSendRejected)
	}
	// ChunkSize(0) prevents the SDK's resumable-media retries for this
	// non-idempotent send. Losing a response must never cause an automatic resend.
	sent, err := service.Users.Messages.Send("me", &gmail.Message{}).
		Media(msg, googleapi.ContentType("message/rfc822"), googleapi.ChunkSize(0)).
		Fields("id").Context(ctx).Do()
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) {
			state := model.ErrUncertain
			if apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != 408 {
				state = model.ErrSendRejected
			}
			return "", fmt.Errorf("%w: Gmail HTTP %d", state, apiErr.Code)
		}
		var authErr *refreshFailure
		if errors.As(err, &authErr) {
			return "", fmt.Errorf("%w: %s", model.ErrSendRejected, authErr.message)
		}
		return "", fmt.Errorf("%w: Gmail submission did not return a valid response", model.ErrUncertain)
	}
	if sent == nil || sent.Id == "" {
		return "", fmt.Errorf("%w: Gmail returned no message ID", model.ErrUncertain)
	}
	return sent.Id, nil
}

func writeMessage(out io.Writer, sender, kindle, filename string, pdf io.Reader) error {
	if !validAddress(sender) || !validAddress(kindle) || filename == "" ||
		strings.ContainsAny(filename, "/\\") || strings.IndexFunc(filename, unicode.IsControl) != -1 ||
		!strings.HasSuffix(strings.ToLower(filename), ".pdf") {
		return errors.New("invalid MIME envelope or attachment filename")
	}
	w := bufio.NewWriter(out)
	body := multipart.NewWriter(w)
	subject := mime.QEncoding.Encode("utf-8", filename[:len(filename)-4])
	_, err := fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nDate: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", sender, kindle, time.Now().UTC().Format(time.RFC1123Z), subject, body.Boundary())
	if err != nil {
		return errors.New("cannot write message headers")
	}
	note, err := body.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=us-ascii"},
	})
	if err != nil {
		return errors.New("cannot create message body")
	}
	if _, err := io.WriteString(note, "The PDF is attached.\r\n"); err != nil {
		return errors.New("cannot write message body")
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", mime.FormatMediaType("application/pdf", map[string]string{"name": filename}))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	h.Set("Content-Transfer-Encoding", "base64")
	part, err := body.CreatePart(h)
	if err != nil {
		return errors.New("cannot create message attachment")
	}
	// 57 input bytes produce the RFC 2045 maximum of 76 base64 characters.
	src := bufio.NewReaderSize(pdf, 32<<10)
	var block [57]byte
	var line [78]byte
	for {
		n, readErr := io.ReadFull(src, block[:])
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return errors.New("cannot read outgoing PDF")
		}
		if n > 0 {
			base64.StdEncoding.Encode(line[:], block[:n])
			k := base64.StdEncoding.EncodedLen(n)
			copy(line[k:], "\r\n")
			if _, err := part.Write(line[:k+2]); err != nil {
				return errors.New("cannot stage PDF attachment")
			}
		}
		if readErr != nil {
			break
		}
	}
	if err := body.Close(); err != nil {
		return errors.New("cannot finalize message")
	}
	return w.Flush()
}
