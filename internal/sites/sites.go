// Package sites manages Debian-convention virtual hosts: enabling and
// disabling via sites-enabled symlinks, and a per-site maintenance mode
// that swaps the symlink to a generated 503 vhost reusing the site's
// listen/server_name/ssl directives.
package sites

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Buco7854/lightngx/internal/fsown"
)

var (
	ErrNotFound      = errors.New("not found in the available directory")
	ErrNotSymlink    = errors.New("enabled entry is not a symlink, refusing to touch it")
	ErrBadName       = errors.New("invalid name")
	ErrExists        = errors.New("target name already exists")
	ErrInMaintenance = errors.New("end maintenance first")
)

type Manager struct {
	available string
	enabled   string
	stateDir  string // <confdir>/.lightngx: generated vhosts + page
	pageSrc   string // optional custom maintenance page to copy from
	stream    bool   // stream{} vhosts summarize by listen→target, not domains
}

type Site struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Maintenance bool     `json:"maintenance"`
	Domains     []string `json:"domains"` // server_name values, or listen→target for streams
}

func New(available, enabled, stateDir, pageSrc string) *Manager {
	return &Manager{available: available, enabled: enabled, stateDir: stateDir, pageSrc: pageSrc}
}

// AsStream marks this manager as handling stream{} vhosts (affects how
// List summarizes each entry) and returns it for chaining.
func (m *Manager) AsStream() *Manager {
	m.stream = true
	return m
}

// Ready reports whether the conventional directories exist.
func (m *Manager) Ready() bool {
	ia, err := os.Stat(m.available)
	if err != nil || !ia.IsDir() {
		return false
	}
	ie, err := os.Stat(m.enabled)
	return err == nil && ie.IsDir()
}

func (m *Manager) validName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\x00") ||
		name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return ErrBadName
	}
	return nil
}

func (m *Manager) maintenanceConf(name string) string {
	return filepath.Join(m.stateDir, "maintenance", name+".conf")
}

// List returns every site in sites-available with its current state.
func (m *Manager) List() ([]Site, error) {
	entries, err := os.ReadDir(m.available)
	if err != nil {
		return nil, err
	}
	var out []Site
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		s := Site{Name: e.Name()}
		link := filepath.Join(m.enabled, e.Name())
		if fi, err := os.Lstat(link); err == nil {
			s.Enabled = true
			if fi.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Readlink(link); err == nil {
					resolved := target
					if !filepath.IsAbs(resolved) {
						resolved = filepath.Join(m.enabled, target)
					}
					if filepath.Clean(resolved) == m.maintenanceConf(e.Name()) {
						s.Maintenance = true
					}
				}
			}
		}
		if src, err := os.ReadFile(filepath.Join(m.available, e.Name())); err == nil {
			s.Domains = summarize(string(src), m.stream)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Enable links sites-enabled/<name> to sites-available/<name> and
// returns a restore function.
func (m *Manager) Enable(name string) (func() error, error) {
	if err := m.validName(name); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(m.available, name)); err != nil {
		return nil, ErrNotFound
	}
	link := filepath.Join(m.enabled, name)
	prev, err := m.snapshotLink(link)
	if err != nil {
		return nil, err
	}
	if err := m.setLink(link, filepath.Join("..", filepath.Base(m.available), name)); err != nil {
		return nil, err
	}
	return prev, nil
}

// Disable removes the sites-enabled symlink and returns a restore function.
func (m *Manager) Disable(name string) (func() error, error) {
	if err := m.validName(name); err != nil {
		return nil, err
	}
	link := filepath.Join(m.enabled, name)
	fi, err := os.Lstat(link)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil, ErrNotSymlink
	}
	target, err := os.Readlink(link)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(link); err != nil {
		return nil, err
	}
	return func() error { return os.Symlink(target, link) }, nil
}

// MaintenanceOn generates the 503 vhost for the site and points the
// sites-enabled symlink at it.
func (m *Manager) MaintenanceOn(name string) (func() error, error) {
	if err := m.validName(name); err != nil {
		return nil, err
	}
	src, err := os.ReadFile(filepath.Join(m.available, name))
	if err != nil {
		return nil, ErrNotFound
	}
	conf, err := GenerateMaintenanceVhost(string(src), m.stateDir)
	if err != nil {
		return nil, err
	}
	if err := m.materializePage(); err != nil {
		return nil, err
	}
	genPath := m.maintenanceConf(name)
	if err := os.MkdirAll(filepath.Dir(genPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(genPath, []byte(conf), 0o644); err != nil {
		return nil, err
	}
	fsown.Chown(genPath)

	link := filepath.Join(m.enabled, name)
	prev, err := m.snapshotLink(link)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(m.enabled, genPath)
	if err != nil {
		rel = genPath
	}
	if err := m.setLink(link, rel); err != nil {
		return nil, err
	}
	return func() error {
		if err := prev(); err != nil {
			return err
		}
		return os.Remove(genPath)
	}, nil
}

// MaintenanceOff points the symlink back at the real site config.
func (m *Manager) MaintenanceOff(name string) (func() error, error) {
	if err := m.validName(name); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(m.available, name)); err != nil {
		return nil, ErrNotFound
	}
	link := filepath.Join(m.enabled, name)
	prev, err := m.snapshotLink(link)
	if err != nil {
		return nil, err
	}
	if err := m.setLink(link, filepath.Join("..", filepath.Base(m.available), name)); err != nil {
		return nil, err
	}
	return prev, nil
}

// Cleanup removes the generated maintenance vhost once it is no longer
// referenced. Call it after a successful MaintenanceOff or Disable.
func (m *Manager) Cleanup(name string) {
	if m.validName(name) != nil {
		return
	}
	_ = os.Remove(m.maintenanceConf(name))
}

// linkState inspects the enabled entry for name: whether it exists as a
// symlink and whether it points at the generated maintenance vhost.
func (m *Manager) linkState(name string) (enabled, maintenance bool, err error) {
	link := filepath.Join(m.enabled, name)
	fi, statErr := os.Lstat(link)
	if errors.Is(statErr, os.ErrNotExist) {
		return false, false, nil
	}
	if statErr != nil {
		return false, false, statErr
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return true, false, ErrNotSymlink
	}
	target, readErr := os.Readlink(link)
	if readErr != nil {
		return true, false, readErr
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(m.enabled, target)
	}
	return true, filepath.Clean(target) == m.maintenanceConf(name), nil
}

// Rename renames an available vhost (and its enabled symlink, when
// present) and returns a restore function. Refused during maintenance.
func (m *Manager) Rename(name, newName string) (func() error, error) {
	if err := m.validName(name); err != nil {
		return nil, err
	}
	if err := m.validName(newName); err != nil {
		return nil, err
	}
	oldAvail := filepath.Join(m.available, name)
	newAvail := filepath.Join(m.available, newName)
	if _, err := os.Stat(oldAvail); err != nil {
		return nil, ErrNotFound
	}
	if _, err := os.Lstat(newAvail); err == nil {
		return nil, ErrExists
	}
	enabled, maintenance, err := m.linkState(name)
	if err != nil {
		return nil, err
	}
	if maintenance {
		return nil, ErrInMaintenance
	}
	if _, err := os.Lstat(filepath.Join(m.enabled, newName)); enabled && err == nil {
		return nil, ErrExists
	}

	if err := os.Rename(oldAvail, newAvail); err != nil {
		return nil, err
	}
	fsown.Chown(newAvail)
	if enabled {
		oldLink := filepath.Join(m.enabled, name)
		newLink := filepath.Join(m.enabled, newName)
		if err := os.Remove(oldLink); err != nil {
			_ = os.Rename(newAvail, oldAvail)
			return nil, err
		}
		if err := os.Symlink(filepath.Join("..", filepath.Base(m.available), newName), newLink); err != nil {
			_ = os.Symlink(filepath.Join("..", filepath.Base(m.available), name), oldLink)
			_ = os.Rename(newAvail, oldAvail)
			return nil, err
		}
	}
	return func() error {
		if enabled {
			if err := os.Remove(filepath.Join(m.enabled, newName)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Symlink(filepath.Join("..", filepath.Base(m.available), name),
				filepath.Join(m.enabled, name)); err != nil {
				return err
			}
		}
		return os.Rename(newAvail, oldAvail)
	}, nil
}

// Delete removes a vhost from the available directory (plus its enabled
// symlink and any generated maintenance vhost) and returns a restore
// function.
func (m *Manager) Delete(name string) (func() error, error) {
	if err := m.validName(name); err != nil {
		return nil, err
	}
	avail := filepath.Join(m.available, name)
	content, err := os.ReadFile(avail)
	if err != nil {
		return nil, ErrNotFound
	}
	var mode os.FileMode = 0o644
	if fi, err := os.Stat(avail); err == nil {
		mode = fi.Mode().Perm()
	}

	link := filepath.Join(m.enabled, name)
	var linkTarget string
	hadLink := false
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return nil, ErrNotSymlink
		}
		linkTarget, _ = os.Readlink(link)
		hadLink = true
	}
	genPath := m.maintenanceConf(name)
	genContent, genErr := os.ReadFile(genPath)
	hadGen := genErr == nil

	if hadLink {
		if err := os.Remove(link); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(avail); err != nil {
		if hadLink {
			_ = os.Symlink(linkTarget, link)
		}
		return nil, err
	}
	if hadGen {
		_ = os.Remove(genPath)
	}

	return func() error {
		if err := os.WriteFile(avail, content, mode); err != nil {
			return err
		}
		if hadGen {
			if err := os.MkdirAll(filepath.Dir(genPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(genPath, genContent, 0o644); err != nil {
				return err
			}
		}
		if hadLink {
			return os.Symlink(linkTarget, link)
		}
		return nil
	}, nil
}

// snapshotLink captures the current state of a sites-enabled entry so it
// can be restored. Only absent entries and symlinks are supported.
func (m *Manager) snapshotLink(link string) (func() error, error) {
	fi, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return func() error {
			if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil, ErrNotSymlink
	}
	target, err := os.Readlink(link)
	if err != nil {
		return nil, err
	}
	return func() error {
		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(target, link)
	}, nil
}

func (m *Manager) setLink(link, target string) error {
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	fsown.Chown(link)
	return nil
}

// materializePage writes the maintenance HTML into the state dir: the
// custom page if configured, the embedded default otherwise.
func (m *Manager) materializePage() error {
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		return err
	}
	fsown.Chown(m.stateDir)
	dst := filepath.Join(m.stateDir, "maintenance.html")
	if m.pageSrc != "" {
		b, err := os.ReadFile(m.pageSrc)
		if err != nil {
			return fmt.Errorf("LN_MAINTENANCE_PAGE: %w", err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		fsown.Chown(dst)
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.WriteFile(dst, []byte(defaultPage), 0o644); err != nil {
		return err
	}
	fsown.Chown(dst)
	return nil
}
