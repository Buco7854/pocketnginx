// Package confdir provides file operations strictly confined to the
// nginx configuration directory, with symlink-escape protection.
package confdir

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Buco7854/lightngx/internal/fsown"
)

var (
	ErrOutsideRoot = errors.New("path escapes the configuration directory")
	ErrTooLarge    = errors.New("file too large to edit")
	ErrBinary      = errors.New("file is not text")
)

type Dir struct {
	root    string
	maxSize int64
}

type Entry struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	IsDir    bool    `json:"isDir"`
	Size     int64   `json:"size,omitempty"`
	Symlink  string  `json:"symlink,omitempty"`
	External bool    `json:"external,omitempty"`
	Children []Entry `json:"children,omitempty"`
}

func New(root string, maxSize int64) (*Dir, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("config dir: %w", err)
	}
	return &Dir{root: resolved, maxSize: maxSize}, nil
}

func (d *Dir) Root() string { return d.root }

// resolve validates a client-supplied relative path and returns the
// absolute on-disk path. The path must stay inside the root both
// lexically and after resolving every symlink on the way.
func (d *Dir) resolve(rel string) (string, error) {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", ErrOutsideRoot
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
		return "", ErrOutsideRoot
	}
	abs := filepath.Join(d.root, clean)

	// Resolve the deepest existing ancestor; anything below it is being
	// created and cannot be a symlink yet.
	probe := abs
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if resolved != d.root && !strings.HasPrefix(resolved, d.root+string(filepath.Separator)) {
				return "", ErrOutsideRoot
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", ErrOutsideRoot
		}
		probe = parent
	}
	return abs, nil
}

// Tree lists the whole config directory as a nested structure.
func (d *Dir) Tree() (Entry, error) {
	return d.walk(d.root, "")
}

func (d *Dir) walk(abs, rel string) (Entry, error) {
	e := Entry{Name: filepath.Base(abs), Path: rel, IsDir: true}
	if rel == "" {
		e.Name = "/"
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return e, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir()
		}
		return items[i].Name() < items[j].Name()
	})
	for _, it := range items {
		childAbs := filepath.Join(abs, it.Name())
		childRel := it.Name()
		if rel != "" {
			childRel = rel + "/" + it.Name()
		}
		info, err := os.Lstat(childAbs)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(childAbs)
			resolved, err := filepath.EvalSymlinks(childAbs)
			external := err != nil || (resolved != d.root && !strings.HasPrefix(resolved, d.root+string(filepath.Separator)))
			st, statErr := os.Stat(childAbs)
			child := Entry{Name: it.Name(), Path: childRel, Symlink: target, External: external}
			if statErr == nil {
				child.IsDir = st.IsDir()
				if !st.IsDir() {
					child.Size = st.Size()
				}
			}
			e.Children = append(e.Children, child)
			continue
		}
		if info.IsDir() {
			child, err := d.walk(childAbs, childRel)
			if err != nil {
				continue
			}
			e.Children = append(e.Children, child)
			continue
		}
		e.Children = append(e.Children, Entry{Name: it.Name(), Path: childRel, Size: info.Size()})
	}
	return e, nil
}

// Read returns the content of a text file inside the root.
func (d *Dir) Read(rel string) ([]byte, error) {
	abs, err := d.resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("is a directory")
	}
	if info.Size() > d.maxSize {
		return nil, ErrTooLarge
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(b, 0) != -1 {
		return nil, ErrBinary
	}
	return b, nil
}

// Write atomically replaces (or creates) a file inside the root and
// returns a restore function that puts the previous state back.
func (d *Dir) Write(rel string, content []byte) (restore func() error, err error) {
	abs, err := d.resolve(rel)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > d.maxSize {
		return nil, ErrTooLarge
	}
	// Follow an in-root symlink so we update its target rather than
	// replacing the link with a regular file (sites-enabled pattern).
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return nil, errors.New("is a directory")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}

	old, oldErr := os.ReadFile(abs)
	existed := oldErr == nil
	var mode os.FileMode = 0o644
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(abs), ".ln-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	if err := os.Rename(tmpName, abs); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	fsown.Chown(abs)

	restore = func() error {
		if existed {
			return atomicWrite(abs, old, mode)
		}
		return os.Remove(abs)
	}
	return restore, nil
}

// atomicWrite replaces abs via a same-dir temp file + rename, so a crash
// mid-write leaves either the old or the new content, never a truncated file.
func atomicWrite(abs string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".ln-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, abs); err != nil {
		os.Remove(name)
		return err
	}
	fsown.Chown(abs)
	return nil
}

// Mkdir creates a directory (and any missing parents) inside the root.
func (d *Dir) Mkdir(rel string) error {
	abs, err := d.resolve(rel)
	if err != nil {
		return err
	}
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return nil
		}
		return errors.New("a file with that name already exists")
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	fsown.Chown(abs)
	return nil
}

// Rename moves a file, symlink or directory inside the root and returns a
// restore function. The destination must not exist. os.Rename moves a whole
// directory subtree atomically on the same filesystem.
func (d *Dir) Rename(fromRel, toRel string) (restore func() error, err error) {
	from, err := d.resolve(fromRel)
	if err != nil {
		return nil, err
	}
	to, err := d.resolve(toRel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(from); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(to); err == nil {
		return nil, errors.New("target already exists")
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(from, to); err != nil {
		return nil, err
	}
	fsown.ChownTree(to)
	return func() error { return os.Rename(to, from) }, nil
}

// Delete removes a file, symlink or directory inside the root and returns a
// restore function. Directories are removed recursively; the restore snapshots
// the subtree first so a failed nginx -t can roll the whole thing back.
func (d *Dir) Delete(rel string) (restore func() error, err error) {
	abs, err := d.resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	// A symlink is removed as the link itself (even one pointing at a dir),
	// never followed, matching how the tree lists it.
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(abs)
		if err := os.Remove(abs); err != nil {
			return nil, err
		}
		return func() error { return os.Symlink(target, abs) }, nil
	}
	if info.IsDir() {
		snap, err := snapshotTree(abs)
		if err != nil {
			return nil, err
		}
		if err := os.RemoveAll(abs); err != nil {
			return nil, err
		}
		return func() error { return restoreTree(abs, snap) }, nil
	}
	old, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	mode := info.Mode().Perm()
	if err := os.Remove(abs); err != nil {
		return nil, err
	}
	return func() error { return os.WriteFile(abs, old, mode) }, nil
}

// snapNode captures one filesystem entry for directory delete/restore.
type snapNode struct {
	rel     string // path relative to the snapshot root ("." for the root)
	mode    os.FileMode
	symlink string
	content []byte
}

// snapshotTree records a directory subtree (files, symlinks and empty dirs)
// without following symlinks, so restoreTree can recreate it exactly.
func snapshotTree(root string) ([]snapNode, error) {
	var nodes []snapNode
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := de.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		n := snapNode{rel: rel, mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if n.symlink, err = os.Readlink(p); err != nil {
				return err
			}
		case info.IsDir():
			// nothing extra; recreated by mode
		default:
			if n.content, err = os.ReadFile(p); err != nil {
				return err
			}
		}
		nodes = append(nodes, n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// restoreTree recreates a subtree captured by snapshotTree. Nodes come in
// WalkDir order (parents before children), so directories exist first.
func restoreTree(root string, nodes []snapNode) error {
	for _, n := range nodes {
		p := filepath.Join(root, n.rel)
		switch {
		case n.mode&os.ModeSymlink != 0:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(n.symlink, p); err != nil {
				return err
			}
		case n.mode.IsDir():
			if err := os.MkdirAll(p, n.mode.Perm()); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, n.content, n.mode.Perm()); err != nil {
				return err
			}
		}
	}
	fsown.ChownTree(root)
	return nil
}
