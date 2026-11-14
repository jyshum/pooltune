package secrets

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxSize = 64 * 1024

// Reader pins an allowlisted directory. File references are absolute paths
// beneath that directory. Rooted opens keep symlinks from escaping it.
type Reader struct {
	path string
	root *os.Root
}

func Open(path string) (*Reader, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("secrets: root path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("secrets: root must be a directory without group/world write access")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		root.Close()
		return nil, errors.New("secrets: root changed while opening")
	}
	return &Reader{path: filepath.Clean(path), root: root}, nil
}

func (r *Reader) Close() error { return r.root.Close() }

func (r *Reader) Read(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("secrets: reference must be absolute")
	}
	rel, err := filepath.Rel(r.path, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("secrets: reference is outside root")
	}
	// O_NONBLOCK ensures an accidental FIFO reference cannot block the gateway.
	f, err := r.root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("secrets: cannot open referenced file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errors.New("secrets: cannot inspect referenced file")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secrets: reference is not a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("secrets: file is accessible to group or others")
	}
	if info.Size() == 0 || info.Size() > maxSize {
		return nil, errors.New("secrets: file size outside allowed range")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, errors.New("secrets: cannot read referenced file")
	}
	if len(data) == 0 || len(data) > maxSize {
		return nil, errors.New("secrets: file size outside allowed range")
	}
	return data, nil
}
