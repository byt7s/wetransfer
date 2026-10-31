// Package zipx packs a directory into a zip the way the CLI sends folders:
// one folder becomes one .zip file inside the transfer.
package zipx

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Result struct {
	Path  string
	Size  int64
	Files int
}

// Dir zips srcDir into dstPath. The archive contains the directory's contents
// at its root. onFile is called after each entry is written.
func Dir(ctx context.Context, srcDir, dstPath string, onFile func()) (*Result, error) {
	type entry struct {
		path  string
		rel   string
		info  os.FileInfo
		isDir bool
	}
	var entries []entry
	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == srcDir {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			entries = append(entries, entry{path: path, rel: rel + "/", info: info, isDir: true})
		case info.Mode().IsRegular():
			entries = append(entries, entry{path: path, rel: rel, info: info})
		default:
			// Symlinks, sockets and devices are skipped: following them could
			// pull in data from outside the folder the user asked to send.
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	f, err := os.Create(dstPath)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(dstPath)
		}
	}()

	zw := zip.NewWriter(f)

	files := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			_ = zw.Close()
			return nil, err
		}
		if e.isDir {
			hdr, err := zip.FileInfoHeader(e.info)
			if err != nil {
				_ = zw.Close()
				return nil, err
			}
			hdr.Name = e.rel
			hdr.Method = zip.Store
			if _, err := zw.CreateHeader(hdr); err != nil {
				_ = zw.Close()
				return nil, err
			}
			continue
		}

		hdr, err := zip.FileInfoHeader(e.info)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		hdr.Name = e.rel
		hdr.Method = zip.Deflate
		if storeExtension(e.rel) {
			hdr.Method = zip.Store
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		src, err := os.Open(e.path)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		_, copyErr := io.Copy(w, src)
		closeErr := src.Close()
		if copyErr != nil {
			_ = zw.Close()
			return nil, copyErr
		}
		if closeErr != nil {
			_ = zw.Close()
			return nil, closeErr
		}
		files++
		if onFile != nil {
			onFile()
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	st, err := os.Stat(dstPath)
	if err != nil {
		return nil, err
	}
	ok = true
	return &Result{
		Path:  dstPath,
		Size:  st.Size(),
		Files: files,
	}, nil
}

func storeExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".avif",
		".mp4", ".mov", ".mkv", ".avi", ".webm",
		".mp3", ".m4a", ".aac", ".flac", ".ogg",
		".zip", ".gz", ".bz2", ".xz", ".7z", ".rar",
		".docx", ".xlsx", ".pptx", ".pdf":
		return true
	}
	return false
}

// NameFor returns the zip filename used in the transfer.
func NameFor(dir string) string {
	base := filepath.Base(filepath.Clean(dir))
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "files"
	}
	return base + ".zip"
}
