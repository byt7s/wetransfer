// Package api speaks to WeTransfer's anonymous web API.
//
// The endpoints mirror what wetransfer.com does in the browser: a transfer is
// created, its files are uploaded as "storm" blocks to S3, the batch is
// announced, and the transfer is consolidated into a shareable link.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

const (
	BaseURL   = "https://wetransfer.com/api/v4"
	webOrigin = "https://wetransfer.com"
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	ErrWrongPassword = errors.New("invalid transfer password")
	ErrNotFound      = errors.New("transfer not found")
)

// Error is a non-2xx answer from the API.
type Error struct {
	Status  int
	Message string
	Body    string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("http %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("http %d", e.Status)
}

// Client is a small JSON client with a cookie jar and a browser user agent.
type Client struct {
	HTTP    *http.Client
	Verbose bool
	Log     io.Writer
}

func New(verbose bool) *Client {
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 32
	tr.MaxIdleConnsPerHost = 16
	return &Client{
		HTTP:    &http.Client{Jar: jar, Transport: tr},
		Verbose: verbose,
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Verbose && c.Log != nil {
		fmt.Fprintf(c.Log, format+"\n", args...)
	}
}

func (c *Client) do(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Origin", webOrigin)
	req.Header.Set("Referer", webOrigin+"/")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.logf("→ %s %s", method, url)
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	c.logf("← %s %s (%s)", resp.Status, url, time.Since(start).Round(time.Millisecond))

	if resp.StatusCode >= 400 {
		return &Error{
			Status:  resp.StatusCode,
			Message: extractMessage(data),
			Body:    string(data),
		}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func extractMessage(data []byte) string {
	var m struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if json.Unmarshal(data, &m) == nil {
		if m.Message != "" {
			return m.Message
		}
		if s, ok := m.Error.(string); ok && s != "" {
			return s
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// FileMeta describes a file at transfer creation time.
type FileMeta struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	ItemType string `json:"item_type"`
}

// FileItem is a file returned by the API.
type FileItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	ItemType string `json:"item_type"`
}

// Transfer is the subset of the transfer object this tool needs.
type Transfer struct {
	ID                  string     `json:"id"`
	State               string     `json:"state"`
	TransferType        int        `json:"transfer_type"`
	ShortenedURL        string     `json:"shortened_url"`
	RecommendedFilename string     `json:"recommended_filename"`
	ExpiresAt           string     `json:"expires_at"`
	PasswordProtected   bool       `json:"password_protected"`
	ExpiryInSeconds     int64      `json:"expiry_in_seconds"`
	Size                int64      `json:"size"`
	DisplayName         string     `json:"display_name"`
	Message             string     `json:"message"`
	SecurityHash        string     `json:"security_hash"`
	StormUploadToken    string     `json:"storm_upload_token"`
	MutationToken       string     `json:"mutation_token"`
	Files               []FileItem `json:"files"`
	Items               []FileItem `json:"items"`
	NumberOfDownloads   int        `json:"number_of_downloads"`
}

type createRequest struct {
	Message           string     `json:"message"`
	Passwordless      bool       `json:"passwordless"`
	AnonymousTransfer bool       `json:"anonymous_transfer"`
	DisplayName       string     `json:"display_name,omitempty"`
	UILanguage        string     `json:"ui_language"`
	DomainUserID      string     `json:"domain_user_id"`
	TotalFileSize     int64      `json:"total_file_size"`
	TotalFileCount    int        `json:"total_file_count"`
	ExpireIn          int64      `json:"expire_in"`
	Password          string     `json:"password,omitempty"`
	Files             []FileMeta `json:"files"`
}

// CreateOptions are the knobs exposed by `wetransfer up`.
type CreateOptions struct {
	Files        []FileMeta
	Message      string
	Title        string
	Password     string
	Expiry       time.Duration
	DomainUserID string
}

// Create starts a transfer and returns it with a storm upload token.
func (c *Client) Create(ctx context.Context, opts CreateOptions) (*Transfer, error) {
	req := createRequest{
		Message:           opts.Message,
		Passwordless:      true,
		AnonymousTransfer: true,
		DisplayName:       opts.Title,
		UILanguage:        "en",
		DomainUserID:      opts.DomainUserID,
		TotalFileSize:     totalSize(opts.Files),
		TotalFileCount:    len(opts.Files),
		ExpireIn:          int64(opts.Expiry.Seconds()),
		Password:          opts.Password,
		Files:             opts.Files,
	}
	var out Transfer
	if err := c.do(ctx, http.MethodPost, BaseURL+"/transfers", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func totalSize(files []FileMeta) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// PrepareDownload returns the transfer summary. Without a password a
// protected transfer still answers, but with no item list.
func (c *Client) PrepareDownload(ctx context.Context, id, hash, password, recipientID string) (*Transfer, error) {
	body := map[string]any{"security_hash": hash}
	if password != "" {
		body["password"] = password
	}
	if recipientID != "" {
		body["recipient_id"] = recipientID
	}
	var out Transfer
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/transfers/%s/prepare-download", BaseURL, id), body, &out)
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) {
			switch {
			case ae.Status == http.StatusForbidden && strings.Contains(strings.ToLower(ae.Message), "password"):
				return nil, ErrWrongPassword
			case ae.Status == http.StatusNotFound:
				return nil, ErrNotFound
			case ae.Status == http.StatusForbidden:
				return nil, fmt.Errorf("access denied: %s", ae.Message)
			}
		}
		return nil, err
	}
	return &out, nil
}

// DirectLink signs a download URL for one file.
func (c *Client) DirectLink(ctx context.Context, id, hash, password, fileID, recipientID string) (string, error) {
	body := map[string]any{
		"security_hash": hash,
		"intent":        "single_file",
		"file_ids":      []string{fileID},
	}
	if fileID == "" {
		body["intent"] = "entire_transfer"
		delete(body, "file_ids")
	}
	if password != "" {
		body["password"] = password
	}
	if recipientID != "" {
		body["recipient_id"] = recipientID
	}
	var out struct {
		DirectLink string `json:"direct_link"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/transfers/%s/download", BaseURL, id), body, &out); err != nil {
		return "", err
	}
	if out.DirectLink == "" {
		return "", errors.New("api returned an empty download link")
	}
	return out.DirectLink, nil
}

// Consolidate finalizes the storm batch and makes the transfer downloadable.
func (c *Client) Consolidate(ctx context.Context, id, mutationToken string) (*Transfer, error) {
	body := map[string]any{"wants_storm": true}
	if mutationToken != "" {
		body["mutation_token"] = mutationToken
	}
	var out Transfer
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("%s/transfers/%s/consolidate", BaseURL, id), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a transfer. Used to clean up after a failed upload.
func (c *Client) Delete(ctx context.Context, id string) error {
	body := map[string]any{"is_user_request": true}
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/transfers/%s", BaseURL, id), body, nil)
}
