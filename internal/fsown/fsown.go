// Package fsown chowns files the UI creates to the nginx worker user.
// Configured once at startup and applied best-effort (chown needs privilege).
package fsown

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// A negative uid means "leave ownership untouched".
var (
	uid atomic.Int64
	gid atomic.Int64
)

func init() {
	uid.Store(-1)
	gid.Store(-1)
}

// Configure sets the owner applied to newly created paths.
func Configure(u, g int) {
	uid.Store(int64(u))
	gid.Store(int64(g))
}

// Enabled reports whether a target owner has been configured.
func Enabled() bool { return uid.Load() >= 0 }

// Chown assigns the configured owner to path, best-effort.
func Chown(path string) {
	u := int(uid.Load())
	if u < 0 {
		return
	}
	_ = os.Lchown(path, u, int(gid.Load()))
}

// ChownTree applies Chown to root and everything under it, without
// following symlinks.
func ChownTree(root string) {
	if !Enabled() {
		return
	}
	_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		Chown(p)
		return nil
	})
}
