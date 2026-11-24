package zipx

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "tree.zip")
	c := &counter{}
	res, err := Dir(context.Background(), src, dst, c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 2 {
		t.Errorf("files=%d", res.Files)
	}
	want := int64(len("alpha\n") + len("beta\n"))
	if c.total != want || c.done != want {
		t.Errorf("progress = %d/%d, want %d/%d", c.done, c.total, want, want)
	}
	if res.Size <= 0 {
		t.Errorf("size = %d", res.Size)
	}

	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	contents := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[f.Name] = string(b)
	}
	if contents["a.txt"] != "alpha\n" {
		t.Errorf("a.txt = %q", contents["a.txt"])
	}
	if contents["sub/b.txt"] != "beta\n" {
		t.Errorf("sub/b.txt = %q", contents["sub/b.txt"])
	}
}

type counter struct {
	total int64
	done  int64
}

func (c *counter) SetTotal(total int64) { c.total = total }
func (c *counter) Add(n int64)          { c.done += n }

func TestNameFor(t *testing.T) {
	if got := NameFor("/tmp/photos"); got != "photos.zip" {
		t.Errorf("NameFor = %q", got)
	}
	if got := NameFor("."); got != "files.zip" {
		t.Errorf("NameFor(.) = %q", got)
	}
}
