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
	var packed int
	res, err := Dir(context.Background(), src, dst, func() { packed++ })
	if err != nil {
		t.Fatal(err)
	}
	if packed != 2 || res.Files != 2 {
		t.Errorf("packed=%d files=%d", packed, res.Files)
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

func TestNameFor(t *testing.T) {
	if got := NameFor("/tmp/photos"); got != "photos.zip" {
		t.Errorf("NameFor = %q", got)
	}
	if got := NameFor("."); got != "files.zip" {
		t.Errorf("NameFor(.) = %q", got)
	}
}
