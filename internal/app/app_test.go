package app

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wetransfer/internal/retry"
)

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"hello.txt":             "hello.txt",
		"../../etc/passwd":      "passwd",
		`..\..\windows\win.ini`: "win.ini",
		"/absolute/path/a.bin":  "a.bin",
		"":                      "file",
		"..":                    "file",
		"a\x00b.txt":            "ab.txt",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := uniquePath(first)
	if filepath.Base(got) != "report (1).pdf" {
		t.Errorf("uniquePath = %q", got)
	}
	if err := os.WriteFile(got, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got2 := uniquePath(first); filepath.Base(got2) != "report (2).pdf" {
		t.Errorf("second uniquePath = %q", got2)
	}
}

func TestResolveDirectURL(t *testing.T) {
	ref, err := ResolveLink(context.Background(), "https://wetransfer.com/downloads/abc123/def456")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "abc123" || ref.Hash != "def456" {
		t.Errorf("ref = %+v", ref)
	}

	ref, err = ResolveLink(context.Background(), "0123456789abcdef/def456")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "0123456789abcdef" || ref.Hash != "def456" {
		t.Errorf("pair ref = %+v", ref)
	}
}

func TestResolveRecipientURL(t *testing.T) {
	ref, err := ResolveLink(context.Background(), "https://wetransfer.com/downloads/abc123/recip456/hash789")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "abc123" || ref.RecipientID != "recip456" || ref.Hash != "hash789" {
		t.Errorf("ref = %+v", ref)
	}

	// A two-part link must not mistake the security hash for a recipient.
	ref, err = ResolveLink(context.Background(), "https://wetransfer.com/downloads/abc123/hash789")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "abc123" || ref.RecipientID != "" || ref.Hash != "hash789" {
		t.Errorf("ref = %+v", ref)
	}

	ref, err = ResolveLink(context.Background(), "0123456789abcdef/recip456/hash789")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "0123456789abcdef" || ref.RecipientID != "recip456" || ref.Hash != "hash789" {
		t.Errorf("pair ref = %+v", ref)
	}
}

func TestDefaultTitle(t *testing.T) {
	if got := defaultTitle([]uploadFile{{name: "hello.txt"}}); got != "hello.txt" {
		t.Errorf("single file title = %q", got)
	}
	if got := defaultTitle([]uploadFile{{name: "photos.zip", packed: true}}); got != "photos" {
		t.Errorf("folder title = %q", got)
	}
	if got := defaultTitle([]uploadFile{{name: "a.txt"}, {name: "b.txt"}}); got != "Transfer" {
		t.Errorf("multi title = %q", got)
	}
}

func TestPlanChunks(t *testing.T) {
	cases := []struct {
		size   int64
		chunks int
		last   int64
	}{
		{0, 0, 0},
		{1024, 1, 1024},
		{uploadBlockSize, 1, uploadBlockSize},
		{uploadBlockSize + 1, 2, 1},
		{3 * uploadBlockSize, 3, uploadBlockSize},
		{3*uploadBlockSize + 123, 4, 123},
	}
	for _, c := range cases {
		chunks := planChunks(c.size)
		if len(chunks) != c.chunks {
			t.Errorf("planChunks(%d) = %d chunks, want %d", c.size, len(chunks), c.chunks)
			continue
		}
		if len(chunks) > 0 && chunks[len(chunks)-1].size != c.last {
			t.Errorf("planChunks(%d) last = %d, want %d", c.size, chunks[len(chunks)-1].size, c.last)
		}
		var total int64
		for _, ch := range chunks {
			total += ch.size
		}
		if total != c.size {
			t.Errorf("planChunks(%d) total = %d", c.size, total)
		}
	}
}

func TestHashChunks(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}
	path := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	chunks := planChunks(1000)
	var got int64
	if err := hashChunks(context.Background(), path, chunks, func(n int64) { got += n }); err != nil {
		t.Fatal(err)
	}
	if got != 1000 {
		t.Errorf("hashed %d bytes", got)
	}
	sum := md5.Sum(data[:1000])
	if chunks[0].md5 != hex.EncodeToString(sum[:]) {
		t.Errorf("md5 = %s", chunks[0].md5)
	}
}

func TestIdleReader(t *testing.T) {
	rc := &blockingReadCloser{ch: make(chan struct{})}
	r := newIdleReader(rc, 20*time.Millisecond)
	defer r.Close()

	start := time.Now()
	_, err := r.Read(make([]byte, 16))
	if !errors.Is(err, retry.ErrStalled) {
		t.Fatalf("Read = %v, want ErrStalled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle timeout took %v", elapsed)
	}
}

type blockingReadCloser struct {
	ch chan struct{}
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	<-b.ch
	return 0, io.EOF
}

func (b *blockingReadCloser) Close() error {
	select {
	case <-b.ch:
	default:
		close(b.ch)
	}
	return nil
}

func TestParallelStopsOnError(t *testing.T) {
	var ran int
	err := parallel(context.Background(), 2, 10, func(i int) error {
		ran++
		if i == 0 {
			return os.ErrInvalid
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if ran >= 10 {
		t.Errorf("ran %d jobs after failure", ran)
	}
}
