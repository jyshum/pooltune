package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadRestrictedMountedFiles(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []os.FileMode{0600, 0400} {
		path := filepath.Join(root, "key")
		if err := os.WriteFile(path, []byte("private-value"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		r, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		value, err := r.Read(path)
		r.Close()
		if err != nil || string(value) != "private-value" {
			t.Fatalf("mode=%o value=%q err=%v", mode, value, err)
		}
	}
}

func TestReadRejectsUnsafePathsFilesAndModes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private-value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose"), []byte("private-value"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", 65537)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "parent")); err != nil {
		t.Fatal(err)
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, path := range []string{outside, filepath.Join(root, "..", filepath.Base(filepath.Dir(outside)), "outside"), filepath.Join(root, "loose"), filepath.Join(root, "empty"), filepath.Join(root, "large"), filepath.Join(root, "escape"), filepath.Join(root, "parent", "outside"), root, "relative"} {
		if value, err := r.Read(path); err == nil {
			t.Errorf("accepted %s: %q", path, value)
		}
	}
}

func TestRootHandleDoesNotFollowReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "keys")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "key")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := r.Read(path)
	if err != nil || string(value) != "old" {
		t.Fatalf("root replacement: %q %v", value, err)
	}
}

func TestOpenRejectsLooseRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if r, err := Open(root); err == nil {
		r.Close()
		t.Fatal("loose root accepted")
	}
}

func TestFIFOIsRejectedWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	go func() { _, err := r.Read(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO read blocked")
	}
}
