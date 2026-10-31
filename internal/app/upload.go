package app

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wetransfer/internal/api"
	"wetransfer/internal/config"
	"wetransfer/internal/ui"
	"wetransfer/internal/zipx"
)

// uploadBlockSize is fixed by storm: every block except the last must be
// exactly 15 MiB. Larger files are split so several connections can upload.
const uploadBlockSize int64 = 15728640

// UploadOptions is a fully resolved upload request.
type UploadOptions struct {
	Paths       []string
	Password    string
	Expiry      time.Duration
	Title       string
	Message     string
	Silent      bool
	Verbose     bool
	Concurrency int
	Err         io.Writer
}

type uploadFile struct {
	path   string
	name   string
	size   int64
	chunks []uploadChunk
}

type uploadChunk struct {
	offset int64
	size   int64
	md5    string
	block  api.Block
}

type chunkJob struct {
	file  *uploadFile
	chunk *uploadChunk
}

// UploadedFile is one item that was sent.
type UploadedFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// UploadResult is a finalized transfer and the files it carries.
type UploadResult struct {
	Transfer *api.Transfer
	Files    []UploadedFile
}

// Upload creates the transfer, uploads every file as storm blocks, and
// finalizes it.
func Upload(ctx context.Context, opts UploadOptions) (*UploadResult, error) {
	if len(opts.Paths) == 0 {
		return nil, errors.New("no files to send")
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 4
	}
	progressOn := !opts.Silent && ui.IsTerminal(os.Stderr)

	files, cleanup, err := collectFiles(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	var total int64
	metas := make([]api.FileMeta, 0, len(files))
	for _, f := range files {
		total += f.size
		metas = append(metas, api.FileMeta{Name: f.name, Size: f.size, ItemType: "file"})
	}
	if total > config.MaxFreeSize {
		return nil, fmt.Errorf("transfer is %s, over the %s anonymous limit",
			ui.FormatBytes(total), ui.FormatBytes(config.MaxFreeSize))
	}
	if len(files) > config.MaxFiles {
		return nil, fmt.Errorf("%d files, over the %d file limit", len(files), config.MaxFiles)
	}

	client := api.New(opts.Verbose)
	client.Log = opts.Err

	if !opts.Silent {
		fmt.Fprintf(opts.Err, "Creating transfer (%d file%s, %s)…\n", len(files), plural(len(files)), ui.FormatBytes(total))
	}
	tr, err := client.Create(ctx, api.CreateOptions{
		Files:        metas,
		Message:      opts.Message,
		Title:        opts.Title,
		Password:     opts.Password,
		Expiry:       opts.Expiry,
		DomainUserID: newUUID(),
	})
	if err != nil {
		return nil, fmt.Errorf("create transfer: %w", err)
	}

	created := true
	fail := func(err error) (*UploadResult, error) {
		if created {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = client.Delete(cleanupCtx, tr.ID)
			cancel()
		}
		return nil, err
	}

	claims, err := api.ParseStormToken(tr.StormUploadToken)
	if err != nil {
		return fail(err)
	}

	// Split every file into 15 MiB blocks and hash each block. Splitting one
	// large file lets several connections work on it at the same time.
	if total > 0 {
		bar := ui.NewProgress(total, progressOn, opts.Err)
		bar.SetLabel("hashing")
		bar.Start()
		err := parallel(ctx, opts.Concurrency, len(files), func(i int) error {
			f := &files[i]
			f.chunks = planChunks(f.size)
			return hashChunks(ctx, f.path, f.chunks, bar.Add)
		})
		bar.Stop()
		if err != nil {
			return fail(err)
		}
	}

	items := make([]api.PreflightItem, 0, len(files))
	for _, f := range files {
		sizes := make([]int64, len(f.chunks))
		for i, c := range f.chunks {
			sizes[i] = c.size
		}
		items = append(items, api.PreflightItem{Path: f.name, ItemType: "file", Blocks: sizes})
	}
	if err := client.Preflight(ctx, claims, tr.StormUploadToken, items); err != nil {
		return fail(fmt.Errorf("preflight: %w", err))
	}

	var jobs []chunkJob
	var hashBlocks []api.HashBlock
	for fi := range files {
		for ci := range files[fi].chunks {
			c := &files[fi].chunks[ci]
			jobs = append(jobs, chunkJob{file: &files[fi], chunk: c})
			hashBlocks = append(hashBlocks, api.HashBlock{Size: c.size, MD5: c.md5})
		}
	}
	if len(jobs) > 0 {
		blocks, err := client.AnnounceBlocks(ctx, claims, tr.StormUploadToken, hashBlocks)
		if err != nil {
			return fail(fmt.Errorf("announce blocks: %w", err))
		}
		for i := range jobs {
			jobs[i].chunk.block = blocks[i]
		}
	}

	if len(jobs) > 0 {
		up := ui.NewProgress(total, progressOn, opts.Err)
		up.SetLabel(fmt.Sprintf("uploading 0/%d", len(jobs)))
		up.Start()
		var done int
		var mu sync.Mutex
		err = parallel(ctx, opts.Concurrency, len(jobs), func(i int) error {
			j := &jobs[i]
			var lastErr error
			for attempt := 0; attempt < 4; attempt++ {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(time.Duration(attempt) * time.Second):
					}
				}
				var sent int64
				err := client.PutBlockRange(ctx, j.chunk.block, j.file.path, j.chunk.offset, j.chunk.size, func(n int64) {
					sent += n
					up.Add(n)
				})
				if err == nil {
					mu.Lock()
					done++
					up.SetLabel(fmt.Sprintf("uploading %d/%d", done, len(jobs)))
					mu.Unlock()
					return nil
				}
				// Bytes counted before a failed attempt are sent again on the
				// next try, so take them back out of the bar.
				if sent > 0 {
					up.Rewind(sent)
				}
				lastErr = err
				if errors.Is(ctx.Err(), context.Canceled) {
					return ctx.Err()
				}
				// A presigned URL that expired mid-upload gets a fresh block.
				if code, ok := api.StatusCode(err); ok && code == 403 {
					fresh, rerr := announceOne(ctx, client, claims, tr.StormUploadToken, j.chunk.size, j.chunk.md5)
					if rerr == nil {
						j.chunk.block = fresh
					}
				}
			}
			return fmt.Errorf("upload %s (block %d): %w", j.file.name, j.chunk.offset/uploadBlockSize+1, lastErr)
		})
		up.Stop()
		if err != nil {
			return fail(err)
		}
	}

	batch := make([]api.BatchItem, 0, len(files))
	for _, f := range files {
		ids := make([]string, 0, len(f.chunks))
		for _, c := range f.chunks {
			ids = append(ids, c.block.BlockID)
		}
		batch = append(batch, api.BatchItem{Path: f.name, BlockIDs: ids})
	}
	if !opts.Silent {
		fmt.Fprintln(opts.Err, "Finishing upload…")
	}
	if err := completeBatch(ctx, client, claims, tr.StormUploadToken, batch); err != nil {
		return fail(fmt.Errorf("finalize blocks: %w", err))
	}

	final, err := consolidate(ctx, client, tr.ID, tr.MutationToken)
	if err != nil {
		return fail(fmt.Errorf("finalize transfer: %w", err))
	}
	created = false
	res := &UploadResult{Transfer: final}
	for _, f := range files {
		res.Files = append(res.Files, UploadedFile{Name: f.name, Size: f.size})
	}
	return res, nil
}

func planChunks(size int64) []uploadChunk {
	if size <= 0 {
		return nil
	}
	chunks := make([]uploadChunk, 0, (size+uploadBlockSize-1)/uploadBlockSize)
	for offset := int64(0); offset < size; offset += uploadBlockSize {
		n := uploadBlockSize
		if remaining := size - offset; remaining < n {
			n = remaining
		}
		chunks = append(chunks, uploadChunk{offset: offset, size: n})
	}
	return chunks
}

func hashChunks(ctx context.Context, path string, chunks []uploadChunk, on func(int64)) error {
	if len(chunks) == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for i := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := md5.New()
		n, err := io.CopyN(h, f, chunks[i].size)
		if err != nil {
			return fmt.Errorf("read %s at %d: %w", filepath.Base(path), chunks[i].offset, err)
		}
		if n != chunks[i].size {
			return fmt.Errorf("file changed while reading (%d bytes at %d)", n, chunks[i].offset)
		}
		chunks[i].md5 = hex.EncodeToString(h.Sum(nil))
		if on != nil {
			on(n)
		}
	}
	return nil
}

func announceOne(ctx context.Context, client *api.Client, claims *api.StormClaims, token string, size int64, sum string) (api.Block, error) {
	out, err := client.AnnounceBlocks(ctx, claims, token, []api.HashBlock{{Size: size, MD5: sum}})
	if err != nil {
		return api.Block{}, err
	}
	return out[0], nil
}

func completeBatch(ctx context.Context, client *api.Client, claims *api.StormClaims, token string, items []api.BatchItem) error {
	delay := time.Second
	var last error
	for attempt := 0; attempt < 30; attempt++ {
		err := client.CompleteBatch(ctx, claims, token, items)
		if err == nil {
			return nil
		}
		var pending *api.BatchPendingError
		if !errors.As(err, &pending) {
			return err
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay += 500 * time.Millisecond
		}
	}
	return fmt.Errorf("blocks never finished processing: %w", last)
}

func consolidate(ctx context.Context, client *api.Client, id, mutation string) (*api.Transfer, error) {
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		tr, err := client.Consolidate(ctx, id, mutation)
		if err == nil {
			if tr.State == "downloadable" || tr.State == "processing" {
				return waitDownloadable(ctx, client, tr)
			}
			return tr, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return nil, last
}

func waitDownloadable(ctx context.Context, client *api.Client, tr *api.Transfer) (*api.Transfer, error) {
	if tr.State == "downloadable" || tr.SecurityHash == "" {
		return tr, nil
	}
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		cur, err := client.PrepareDownload(ctx, tr.ID, tr.SecurityHash, "", "")
		if err != nil {
			continue
		}
		if cur.State == "downloadable" {
			if cur.ShortenedURL == "" {
				cur.ShortenedURL = tr.ShortenedURL
			}
			return cur, nil
		}
	}
	return tr, nil
}

func collectFiles(ctx context.Context, opts UploadOptions) ([]uploadFile, func(), error) {
	var files []uploadFile
	var temps []string
	cleanup := func() {
		for _, p := range temps {
			_ = os.Remove(p)
		}
	}
	seen := map[string]string{}

	for _, p := range opts.Paths {
		info, err := os.Stat(p)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		switch {
		case info.IsDir():
			name := zipx.NameFor(p)
			tmp, err := os.CreateTemp("", "wetransfer-*.zip")
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			tmpPath := tmp.Name()
			_ = tmp.Close()
			if !opts.Silent {
				fmt.Fprintf(opts.Err, "Packing %s…\n", filepath.Base(filepath.Clean(p)))
			}
			var packed int
			res, err := zipx.Dir(ctx, p, tmpPath, func() { packed++ })
			if err != nil {
				_ = os.Remove(tmpPath)
				cleanup()
				return nil, nil, fmt.Errorf("pack %s: %w", p, err)
			}
			temps = append(temps, res.Path)
			if prev, dup := seen[name]; dup {
				cleanup()
				return nil, nil, fmt.Errorf("duplicate file name %q (%s and %s); rename one of them", name, prev, p)
			}
			if !opts.Silent {
				fmt.Fprintf(opts.Err, "Packed %s (%d files, %s)\n", name, res.Files, ui.FormatBytes(res.Size))
			}
			files = append(files, uploadFile{path: res.Path, name: name, size: res.Size})
			seen[name] = p
		case info.Mode().IsRegular():
			name := filepath.Base(p)
			if prev, dup := seen[name]; dup {
				cleanup()
				return nil, nil, fmt.Errorf("duplicate file name %q (%s and %s); rename one of them", name, prev, p)
			}
			files = append(files, uploadFile{path: p, name: name, size: info.Size()})
			seen[name] = p
		default:
			cleanup()
			return nil, nil, fmt.Errorf("%s is not a regular file or directory", p)
		}
	}
	return files, cleanup, nil
}

// parallel runs fn for [0,n) with at most w workers. It stops at the first
// error and cancels the remaining work.
func parallel(ctx context.Context, w, n int, fn func(int) error) error {
	if n == 0 {
		return nil
	}
	if w > n {
		w = n
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for i := 0; i < w; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := fn(idx); err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
dispatch:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
