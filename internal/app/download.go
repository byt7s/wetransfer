package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wetransfer/internal/api"
	"wetransfer/internal/retry"
	"wetransfer/internal/ui"
)

// DownloadOptions is a fully resolved download request.
type DownloadOptions struct {
	Link        string
	Password    string
	OutputDir   string
	Overwrite   bool
	KeepBoth    bool
	Silent      bool
	Verbose     bool
	Concurrency int
	Stdout      io.Writer
	Err         io.Writer
}

// DownloadedFile is one file saved by Download.
type DownloadedFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// DirectFile is a signed download URL, without downloading anything.
type DirectFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

// TransferInfo is the summary printed by `info`.
type TransferInfo struct {
	URL               string     `json:"url"`
	Title             string     `json:"title,omitempty"`
	Message           string     `json:"message,omitempty"`
	PasswordProtected bool       `json:"password_protected"`
	Size              int64      `json:"size"`
	ExpiresAt         string     `json:"expires_at,omitempty"`
	ExpiryInSeconds   int64      `json:"expiry_in_seconds,omitempty"`
	State             string     `json:"state"`
	Downloads         int        `json:"number_of_downloads"`
	Files             []FileInfo `json:"files,omitempty"`
	FilesHidden       bool       `json:"files_hidden,omitempty"`
}

// FileInfo is one file listed by `info`.
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ErrCancelled means the user chose to stop at an overwrite prompt.
var ErrCancelled = errors.New("cancelled")

// Fetch resolves a link and prepares its download metadata. When allowPrompt
// is false a protected transfer is returned as a summary without its files.
func Fetch(ctx context.Context, link, password string, verbose bool, errOut io.Writer, allowPrompt bool) (*Ref, *api.Transfer, string, error) {
	ref, err := ResolveLink(ctx, link)
	if err != nil {
		return nil, nil, "", err
	}
	client := api.New(verbose)
	client.Log = errOut

	tr, pwd, err := fetchTransfer(ctx, client, ref, password, errOut, allowPrompt)
	if err != nil {
		return nil, nil, "", err
	}
	return ref, tr, pwd, nil
}

func fetchTransfer(ctx context.Context, client *api.Client, ref *Ref, password string, errOut io.Writer, allowPrompt bool) (*api.Transfer, string, error) {
	tr, err := client.PrepareDownload(ctx, ref.ID, ref.Hash, password, ref.RecipientID)
	if err != nil {
		if !errors.Is(err, api.ErrWrongPassword) {
			return nil, "", err
		}
		if !allowPrompt || !ui.IsTerminal(os.Stdin) {
			return nil, "", err
		}
		fmt.Fprintln(errOut, "Wrong password.")
		return promptAndPrepare(ctx, client, ref, errOut)
	}
	if tr.PasswordProtected && len(tr.Items) == 0 && allowPrompt {
		if !ui.IsTerminal(os.Stdin) {
			return nil, "", errors.New("transfer is password protected; pass -p PASSWORD")
		}
		return promptAndPrepare(ctx, client, ref, errOut)
	}
	return tr, password, nil
}

func promptAndPrepare(ctx context.Context, client *api.Client, ref *Ref, errOut io.Writer) (*api.Transfer, string, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		p, err := promptPassword()
		if err != nil {
			return nil, "", err
		}
		tr, err := client.PrepareDownload(ctx, ref.ID, ref.Hash, p, ref.RecipientID)
		if err == nil {
			return tr, p, nil
		}
		if !errors.Is(err, api.ErrWrongPassword) {
			return nil, "", err
		}
		last = err
		fmt.Fprintln(errOut, "Wrong password.")
	}
	return nil, "", last
}

func promptPassword() (string, error) {
	p, err := ui.ReadPassword("Password: ")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("password required")
	}
	return p, nil
}

// GetInfo reads a transfer summary without downloading anything.
func GetInfo(ctx context.Context, link, password string, verbose bool, errOut io.Writer) (*TransferInfo, error) {
	ref, tr, _, err := Fetch(ctx, link, password, verbose, errOut, false)
	if err != nil {
		return nil, err
	}

	displayURL := tr.ShortenedURL
	if displayURL == "" {
		displayURL = ref.URL
		if i := strings.IndexByte(displayURL, '?'); i >= 0 {
			displayURL = displayURL[:i]
		}
	}
	info := &TransferInfo{
		URL:               displayURL,
		Title:             tr.DisplayName,
		Message:           tr.Message,
		PasswordProtected: tr.PasswordProtected,
		Size:              tr.Size,
		ExpiresAt:         tr.ExpiresAt,
		ExpiryInSeconds:   tr.ExpiryInSeconds,
		State:             tr.State,
		Downloads:         tr.NumberOfDownloads,
	}
	if len(tr.Items) == 0 {
		info.FilesHidden = tr.PasswordProtected
		return info, nil
	}
	for _, it := range tr.Items {
		info.Files = append(info.Files, FileInfo{Name: it.Name, Size: it.Size})
	}
	return info, nil
}

// FormatExpiry renders an RFC3339 expiry with the time left.
func FormatExpiry(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	left := time.Until(t)
	if left <= 0 {
		return t.Local().Format("2006-01-02 15:04") + " (expired)"
	}
	return fmt.Sprintf("%s (%s left)", t.Local().Format("2006-01-02 15:04"), humanLeft(left))
}

func humanLeft(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		hours := int(d/time.Hour) % 24
		return fmt.Sprintf("%dd %dh", days, hours)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d/time.Minute)%60)
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}

// DirectLinks signs every file in the transfer without saving anything.
func DirectLinks(ctx context.Context, link, password string, verbose bool, errOut io.Writer) ([]DirectFile, error) {
	ref, tr, pwd, err := Fetch(ctx, link, password, verbose, errOut, true)
	if err != nil {
		return nil, err
	}
	if tr.State != "downloadable" {
		return nil, fmt.Errorf("transfer is %s, not downloadable", tr.State)
	}
	if len(tr.Items) == 0 {
		return nil, errors.New("transfer has no files")
	}
	client := api.New(verbose)
	client.Log = errOut

	out := make([]DirectFile, 0, len(tr.Items))
	for _, item := range tr.Items {
		url, err := signLink(ctx, client, ref, pwd, item)
		if err != nil {
			return nil, fmt.Errorf("sign %s: %w", item.Name, err)
		}
		out = append(out, DirectFile{Name: item.Name, Size: item.Size, URL: url})
	}
	return out, nil
}

// Download saves every file in the transfer to OutputDir, resuming .part
// files when they exist. An OutputDir of "-" writes a single file to stdout.
func Download(ctx context.Context, opts DownloadOptions) ([]DownloadedFile, error) {
	if opts.Concurrency < 1 {
		opts.Concurrency = 3
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "."
	}

	ref, tr, pwd, err := Fetch(ctx, opts.Link, opts.Password, opts.Verbose, opts.Err, true)
	if err != nil {
		return nil, err
	}
	if tr.State != "downloadable" {
		return nil, fmt.Errorf("transfer is %s, not downloadable", tr.State)
	}
	if len(tr.Items) == 0 {
		if tr.PasswordProtected {
			return nil, errors.New("could not read the file list; check the password")
		}
		return nil, errors.New("transfer has no files")
	}

	client := api.New(opts.Verbose)
	client.Log = opts.Err

	if opts.OutputDir == "-" {
		return streamToStdout(ctx, client, ref, pwd, tr, opts.Stdout)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, err
	}

	jobs := make([]downloadJob, 0, len(tr.Items))
	used := map[string]bool{}
	var total int64
	for _, item := range tr.Items {
		name := safeName(item.Name)
		dst := filepath.Join(opts.OutputDir, name)
		final, err := chooseDestination(dst, opts)
		if err != nil {
			return nil, err
		}
		if used[final] {
			final = uniqueForUsed(final, used)
		}
		used[final] = true
		jobs = append(jobs, downloadJob{item: item, dst: final})
		total += item.Size
	}

	progressOn := !opts.Silent && total > 0 && ui.IsTerminal(os.Stderr)
	bar := ui.NewProgress(total, progressOn, opts.Err)
	bar.SetLabel(fmt.Sprintf("downloading 0/%d", len(jobs)))
	bar.Start()

	results := make([]DownloadedFile, len(jobs))
	var done int
	var mu sync.Mutex
	var waitOnce sync.Once
	onWait := func() {
		waitOnce.Do(func() {
			if progressOn {
				bar.SetLabel("connection lost — retrying…")
			} else if opts.Err != nil {
				fmt.Fprintln(opts.Err, retry.WaitNotice)
			}
		})
	}
	err = parallel(ctx, opts.Concurrency, len(jobs), func(i int) error {
		j := &jobs[i]
		if err := downloadOne(ctx, client, ref, pwd, j, bar.Add, onWait); err != nil {
			return err
		}
		results[i] = DownloadedFile{Name: filepath.Base(j.dst), Path: j.dst, Size: j.item.Size}
		mu.Lock()
		done++
		bar.SetLabel(fmt.Sprintf("downloading %d/%d", done, len(jobs)))
		mu.Unlock()
		return nil
	})
	bar.Stop()
	if err != nil {
		return nil, err
	}
	return results, nil
}

func streamToStdout(ctx context.Context, client *api.Client, ref *Ref, pwd string, tr *api.Transfer, out io.Writer) ([]DownloadedFile, error) {
	if len(tr.Items) != 1 {
		return nil, errors.New("output to stdout needs a transfer with exactly one file")
	}
	if out == nil {
		out = os.Stdout
	}
	item := tr.Items[0]
	url, err := signLink(ctx, client, ref, pwd, item)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", api.UserAgent)
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &api.Error{Status: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return nil, err
	}
	if item.Size > 0 && n != item.Size {
		return nil, fmt.Errorf("incomplete file: %d of %d bytes", n, item.Size)
	}
	return []DownloadedFile{{Name: item.Name, Path: "-", Size: n}}, nil
}

type downloadJob struct {
	item api.FileItem
	dst  string
}

func signLink(ctx context.Context, client *api.Client, ref *Ref, pwd string, item api.FileItem) (string, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		url, err := client.DirectLink(ctx, ref.ID, ref.Hash, pwd, item.ID, ref.RecipientID)
		if err == nil {
			return url, nil
		}
		last = err
		if !retry.IsTemporary(err) {
			return "", err
		}
	}
	return "", last
}

func downloadOne(ctx context.Context, client *api.Client, ref *Ref, pwd string, j *downloadJob, onWrite func(int64), onWait func()) error {
	watch := retry.NewWatch()
	delay := watch.Step()
	for {
		url, err := signLink(ctx, client, ref, pwd, j.item)
		if err != nil {
			if !retry.IsTemporary(err) {
				return fmt.Errorf("download %s: %w", j.item.Name, err)
			}
		} else {
			// A failed attempt leaves its bytes in dst.part, so the next one
			// resumes with a Range request instead of starting over.
			err = fetchToFile(ctx, client.HTTP, url, j.dst, j.item.Size, onWrite)
			if err == nil {
				watch.Mark()
				return nil
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}
			if !retry.IsTemporary(err) {
				return fmt.Errorf("download %s: %w", j.item.Name, err)
			}
		}
		if err := watch.Arm(); err != nil {
			return fmt.Errorf("download %s: %w", j.item.Name, err)
		}
		if onWait != nil {
			onWait()
		}
		if err := retry.Sleep(ctx, delay); err != nil {
			return err
		}
		delay = retry.Next(delay, watch.Cap())
	}
}

func fetchToFile(ctx context.Context, hc *http.Client, url, dst string, size int64, onWrite func(int64)) error {
	part := dst + ".part"

	offset := int64(0)
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
		if size > 0 && offset > size {
			_ = os.Remove(part)
			offset = 0
		}
		if size > 0 && offset == size {
			if err := os.Rename(part, dst); err != nil {
				return err
			}
			if onWrite != nil {
				onWrite(size)
			}
			return nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", api.UserAgent)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	body := newIdleReader(resp.Body, 60*time.Second)
	defer body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusOK:
		offset = 0
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && size > 0 && offset == size:
		return os.Rename(part, dst)
	default:
		msg, _ := io.ReadAll(io.LimitReader(body, 512))
		return &api.Error{Status: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	w := &countingWriter{w: f, on: onWrite}
	_, copyErr := io.CopyBuffer(w, body, make([]byte, 256<<10))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if size > 0 && offset+w.n != size {
		return retry.IncompleteError{Got: offset + w.n, Want: size}
	}
	return os.Rename(part, dst)
}

// idleReader aborts a download when no bytes arrive for d. Closing the body
// unblocks the pending Read; the retry loop then resumes from the .part file.
type idleReader struct {
	rc       io.ReadCloser
	timeout  time.Duration
	timer    *time.Timer
	timedOut atomic.Bool
}

func newIdleReader(rc io.ReadCloser, d time.Duration) *idleReader {
	r := &idleReader{rc: rc, timeout: d}
	r.timer = time.AfterFunc(d, func() {
		r.timedOut.Store(true)
		_ = rc.Close()
	})
	return r
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if err != nil {
		if r.timedOut.Load() {
			return n, retry.ErrStalled
		}
		return n, err
	}
	if r.timedOut.Load() {
		return n, retry.ErrStalled
	}
	r.timer.Reset(r.timeout)
	return n, nil
}

func (r *idleReader) Close() error {
	r.timer.Stop()
	return r.rc.Close()
}

type countingWriter struct {
	w  io.Writer
	n  int64
	on func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.n += int64(n)
		if c.on != nil {
			c.on(int64(n))
		}
	}
	return n, err
}

func chooseDestination(dst string, opts DownloadOptions) (string, error) {
	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
		return dst, nil
	} else if err != nil {
		return "", err
	}
	if opts.Overwrite {
		return dst, nil
	}
	if opts.KeepBoth || !ui.IsTerminal(os.Stdin) {
		return uniquePath(dst), nil
	}

	fmt.Fprintf(opts.Err, "File exists: %s\n", dst)
	for {
		fmt.Fprint(opts.Err, "  [o]verwrite  [k]eep both  [r]ename  [c]ancel  [k]: ")
		line, err := readLine()
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "k":
			return uniquePath(dst), nil
		case "o":
			return dst, nil
		case "r":
			fmt.Fprint(opts.Err, "New name: ")
			name, err := readLine()
			if err != nil {
				return "", err
			}
			name = safeName(name)
			if name == "" || name == "." {
				continue
			}
			return filepath.Join(filepath.Dir(dst), name), nil
		case "c":
			return "", ErrCancelled
		}
	}
}

func safeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

func uniquePath(path string) string {
	return uniqueForUsed(path, nil)
}

// uniqueForUsed finds a name that is neither on disk nor already claimed in
// this download.
func uniqueForUsed(path string, used map[string]bool) string {
	dir, base := filepath.Dir(path), filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if used != nil && used[cand] {
			continue
		}
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
}

func readLine() (string, error) {
	var b strings.Builder
	var buf [1]byte
	for {
		n, err := os.Stdin.Read(buf[:])
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			if buf[0] != '\r' {
				b.WriteByte(buf[0])
			}
		}
		if err != nil {
			if b.Len() > 0 {
				return b.String(), nil
			}
			return "", err
		}
	}
}
