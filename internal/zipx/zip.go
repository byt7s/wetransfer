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

// Counter receives the source byte total and how many bytes have been
// archived so far. *ui.Progress satisfies it.
type Counter interface {
	SetTotal(total int64)
	Add(n int64)
}

// Dir zips srcDir into dstPath. The archive contains the directory's contents
// at its root.
func Dir(ctx context.Context, srcDir, dstPath string, progress Counter) (*Result, error) {
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

	var total int64
	for _, e := range entries {
		if !e.isDir {
			total += e.info.Size()
		}
	}
	if progress != nil {
		progress.SetTotal(total)
	}

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
		buf := make([]byte, 256*1024)
		var copyErr, closeErr error
		for {
			n, readErr := src.Read(buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					copyErr = err
					break
				}
				if progress != nil {
					progress.Add(int64(n))
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					copyErr = readErr
				}
				break
			}
		}
		closeErr = src.Close()
		if copyErr != nil {
			_ = zw.Close()
			return nil, copyErr
		}
		if closeErr != nil {
			_ = zw.Close()
			return nil, closeErr
		}
		files++
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
