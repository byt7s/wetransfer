package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"wetransfer/internal/api"
)

// Ref identifies a transfer from a link, a handle, or an id/hash pair.
// RecipientID is set for links received by email, which look like
// /downloads/{id}/{recipient_id}/{security_hash}.
type Ref struct {
	ID          string
	RecipientID string
	Hash        string
	URL         string
}

var (
	downloadPathRE = regexp.MustCompile(`/downloads/([A-Za-z0-9]+)(?:/([A-Za-z0-9]+))?/([A-Za-z0-9]+)`)
	pairRE         = regexp.MustCompile(`^([A-Za-z0-9]{16,})(?:/([A-Za-z0-9]{4,12}))?/([A-Za-z0-9]{4,12})$`)
	handleRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}$`)
)

// ResolveLink accepts:
//
//	https://we.tl/t-xxxxxxxxxxxx
//	we.tl/t-xxxxxxxxxxxx
//	t-xxxxxxxxxxxx
//	https://wetransfer.com/downloads/<id>/<hash>
//	https://wetransfer.com/downloads/<id>/<recipient>/<hash>
//	<id>/<hash> or <id>/<recipient>/<hash>
func ResolveLink(ctx context.Context, input string) (*Ref, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty link")
	}

	if m := downloadPathRE.FindStringSubmatch(input); m != nil {
		return &Ref{ID: m[1], RecipientID: m[2], Hash: m[3], URL: input}, nil
	}
	if m := pairRE.FindStringSubmatch(input); m != nil {
		return &Ref{ID: m[1], RecipientID: m[2], Hash: m[3], URL: transferURL(m[1], m[2], m[3])}, nil
	}

	url := input
	switch {
	case strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://"):
	case strings.HasPrefix(url, "we.tl/"):
		url = "https://" + url
	case strings.HasPrefix(url, "wetransfer.com/") || strings.HasPrefix(url, "www.wetransfer.com/"):
		url = "https://" + url
	case strings.HasPrefix(url, "t-") && handleRE.MatchString(url):
		url = "https://we.tl/" + url
	case handleRE.MatchString(url):
		url = "https://we.tl/" + url
	default:
		return nil, fmt.Errorf("unrecognized link %q", input)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", api.UserAgent)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve link: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	final := resp.Request.URL.String()
	if m := downloadPathRE.FindStringSubmatch(final); m != nil {
		return &Ref{ID: m[1], RecipientID: m[2], Hash: m[3], URL: final}, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("link returned http %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("could not find a transfer id in %q", final)
}

func transferURL(id, recipient, hash string) string {
	if recipient != "" {
		return fmt.Sprintf("https://wetransfer.com/downloads/%s/%s/%s", id, recipient, hash)
	}
	return fmt.Sprintf("https://wetransfer.com/downloads/%s/%s", id, hash)
}
