package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// StormClaims are the upload endpoints embedded in the storm_upload_token JWT.
type StormClaims struct {
	PreflightURL      string `json:"storm.preflight_batch_url"`
	AnnounceBlocksURL string `json:"storm.announce_blocks_url"`
	CreateBatchURL    string `json:"storm.create_batch_url"`
	ForeignID         string `json:"storm.foreign_id"`
	ExpiresAt         int64  `json:"exp"`
}

// ParseStormToken decodes the JWT without verifying its signature. The token
// was just handed to us by the API over TLS, so there is nothing to verify.
func ParseStormToken(token string) (*StormClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed storm token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, fmt.Errorf("decode storm token: %w", err)
		}
	}
	var claims StormClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode storm token: %w", err)
	}
	if claims.PreflightURL == "" || claims.AnnounceBlocksURL == "" || claims.CreateBatchURL == "" {
		return nil, fmt.Errorf("storm token is missing upload endpoints")
	}
	return &claims, nil
}

type stormError struct {
	Status  int
	Message string
}

func (e *stormError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("storm http %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("storm http %d", e.Status)
}

// StatusCode lets retry classify HTTP failures.
func (e *stormError) StatusCode() int { return e.Status }

// BatchPendingError means the blocks were uploaded but S3 has not finished
// processing them yet. The caller should retry CompleteBatch.
type BatchPendingError struct {
	Message string
}

func (e *BatchPendingError) Error() string { return e.Message }

// StatusCode extracts an HTTP status from any API or storm error.
func StatusCode(err error) (int, bool) {
	var se *stormError
	if errors.As(err, &se) {
		return se.Status, true
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Status, true
	}
	return 0, false
}

func (c *Client) stormDo(ctx context.Context, method, url, token string, in, out any) error {
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
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.logf("→ %s %s", method, url)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	c.logf("← %s %s", resp.Status, url)

	if resp.StatusCode == http.StatusTooEarly {
		var pending struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &pending)
		return &BatchPendingError{Message: pending.Error.Message}
	}
	if resp.StatusCode >= 400 {
		return &stormError{Status: resp.StatusCode, Message: extractMessage(data)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode storm response: %w", err)
		}
	}
	return nil
}

// PreflightItem declares one uploaded file. Blocks holds the byte length of
// each block; storm requires every block except the last to be exactly
// 15 MiB. Empty files have no blocks.
type PreflightItem struct {
	Path     string
	ItemType string
	Blocks   []int64
}

type preflightItem struct {
	Path     string        `json:"path"`
	ItemType string        `json:"item_type"`
	Blocks   []blockLength `json:"blocks"`
}

type blockLength struct {
	ContentLength int64 `json:"content_length"`
}

// Preflight declares all files before any bytes are sent.
func (c *Client) Preflight(ctx context.Context, claims *StormClaims, token string, items []PreflightItem) error {
	req := struct {
		Items []preflightItem `json:"items"`
	}{}
	for _, it := range items {
		blocks := make([]blockLength, len(it.Blocks))
		for i, size := range it.Blocks {
			blocks[i] = blockLength{ContentLength: size}
		}
		req.Items = append(req.Items, preflightItem{
			Path:     it.Path,
			ItemType: "file",
			Blocks:   blocks,
		})
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.stormDo(ctx, http.MethodPost, claims.PreflightURL, token, req, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("preflight was refused")
	}
	return nil
}

// HashBlock is one block announcement.
type HashBlock struct {
	Size int64
	MD5  string // hex
}

// Block is an upload slot: a presigned S3 PUT plus its block id.
type Block struct {
	PresignedPutURL     string            `json:"presigned_put_url"`
	BlockID             string            `json:"block_id"`
	BlockKey            string            `json:"block_key"`
	MustUploadInSeconds int64             `json:"must_upload_in_seconds"`
	PutRequestHeaders   map[string]string `json:"put_request_headers"`
}

// AnnounceBlocks asks storm for upload slots and presigned PUT URLs.
func (c *Client) AnnounceBlocks(ctx context.Context, claims *StormClaims, token string, blocks []HashBlock) ([]Block, error) {
	type announceBlock struct {
		ContentLength int64  `json:"content_length"`
		ContentMD5Hex string `json:"content_md5_hex"`
	}
	req := struct {
		Blocks []announceBlock `json:"blocks"`
	}{}
	for _, b := range blocks {
		req.Blocks = append(req.Blocks, announceBlock{ContentLength: b.Size, ContentMD5Hex: b.MD5})
	}
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			Blocks []Block `json:"blocks"`
		} `json:"data"`
	}
	if err := c.stormDo(ctx, http.MethodPost, claims.AnnounceBlocksURL, token, req, &out); err != nil {
		return nil, err
	}
	if !out.OK || len(out.Data.Blocks) != len(blocks) {
		return nil, fmt.Errorf("storm returned %d blocks, expected %d", len(out.Data.Blocks), len(blocks))
	}
	return out.Data.Blocks, nil
}

// BatchItem ties an uploaded block to its path in the transfer.
type BatchItem struct {
	Path     string
	BlockIDs []string
}

// CompleteBatch tells storm that all blocks are uploaded. It commonly answers
// 425 for a few seconds while S3 processes the blocks; retry on
// *BatchPendingError.
func (c *Client) CompleteBatch(ctx context.Context, claims *StormClaims, token string, items []BatchItem) error {
	type batchItem struct {
		BlockIDs []string `json:"block_ids"`
		ItemType string   `json:"item_type"`
		Path     string   `json:"path"`
	}
	req := struct {
		Items []batchItem `json:"items"`
	}{}
	for _, it := range items {
		req.Items = append(req.Items, batchItem{
			BlockIDs: it.BlockIDs,
			ItemType: "file",
			Path:     it.Path,
		})
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.stormDo(ctx, http.MethodPost, claims.CreateBatchURL, token, req, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("storm refused the batch")
	}
	return nil
}

type countingReader struct {
	r  io.Reader
	on func(int64)
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 && cr.on != nil {
		cr.on(int64(n))
	}
	return n, err
}

// PutBlock streams a whole file to its presigned S3 URL.
func (c *Client) PutBlock(ctx context.Context, block Block, path string, onWrite func(int64)) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return c.PutBlockRange(ctx, block, path, 0, fi.Size(), onWrite)
}

// PutBlockRange streams one block (a byte range of a file) to S3. onWrite is
// called with byte counts for progress.
func (c *Client) PutBlockRange(ctx context.Context, block Block, path string, offset, size int64, onWrite func(int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	section := io.NewSectionReader(f, offset, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, block.PresignedPutURL, &countingReader{r: section, on: onWrite})
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range block.PutRequestHeaders {
		req.Header.Set(k, v)
	}

	c.logf("→ PUT %s (%d bytes)", redactURL(block.PresignedPutURL), size)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	c.logf("← %s PUT upload", resp.Status)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &stormError{
			Status:  resp.StatusCode,
			Message: strings.TrimSpace(string(body)),
		}
	}
	return nil
}

func redactURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i] + "?…"
	}
	return u
}
