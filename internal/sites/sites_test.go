package sites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSite = `
# comment with server { inside
upstream backend { server 127.0.0.1:8080; }

server {
    listen 80;
    listen [::]:80;
    server_name example.com www.example.com; # trailing comment
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name example.com www.example.com;
    ssl_certificate /etc/ssl/domains/example.com/fullchain.pem;
    ssl_certificate_key "/etc/ssl/domains/example.com/key.pem";
    location / {
        proxy_pass http://backend;
        # server_name inside a location must not leak out
    }
}
`

func TestGenerateMaintenanceVhost(t *testing.T) {
	out, err := GenerateMaintenanceVhost(sampleSite, "/etc/nginx/.lightngx")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "server {"); got != 2 {
		t.Fatalf("want 2 server blocks, got %d:\n%s", got, out)
	}
	for _, want := range []string{
		"listen 443 ssl;",
		"listen [::]:443 ssl;",
		"http2 on;",
		"server_name example.com www.example.com;",
		"ssl_certificate /etc/ssl/domains/example.com/fullchain.pem;",
		`ssl_certificate_key "/etc/ssl/domains/example.com/key.pem";`,
		"error_page 503 /maintenance.html;",
		"return 503;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, reject := range []string{"proxy_pass", "return 301", "upstream"} {
		if strings.Contains(out, reject) {
			t.Errorf("%q must not be copied:\n%s", reject, out)
		}
	}
}

func TestGenerateNoServerBlock(t *testing.T) {
	if _, err := GenerateMaintenanceVhost("upstream x { server 1.2.3.4; }", "/tmp"); err == nil {
		t.Fatal("want error for config without server block")
	}
}

func setup(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"sites-available", "sites-enabled"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sites-available/blog"), []byte(sampleSite), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(filepath.Join(root, "sites-available"), filepath.Join(root, "sites-enabled"),
		filepath.Join(root, ".lightngx"), "")
	return m, root
}

func TestEnableDisable(t *testing.T) {
	m, root := setup(t)
	if _, err := m.Enable("blog"); err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil || len(list) != 1 || !list[0].Enabled || list[0].Maintenance {
		t.Fatalf("after enable: %+v err=%v", list, err)
	}
	target, err := os.Readlink(filepath.Join(root, "sites-enabled/blog"))
	if err != nil || target != "../sites-available/blog" {
		t.Fatalf("symlink target = %q err=%v", target, err)
	}

	restore, err := m.Disable("blog")
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := m.List(); list[0].Enabled {
		t.Fatal("still enabled after disable")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.List(); !list[0].Enabled {
		t.Fatal("restore did not re-enable")
	}
}

func TestMaintenanceToggle(t *testing.T) {
	m, root := setup(t)
	if _, err := m.Enable("blog"); err != nil {
		t.Fatal(err)
	}
	restore, err := m.MaintenanceOn("blog")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := m.List()
	if !list[0].Enabled || !list[0].Maintenance {
		t.Fatalf("after maintenance on: %+v", list)
	}
	gen := filepath.Join(root, ".lightngx/maintenance/blog.conf")
	if _, err := os.Stat(gen); err != nil {
		t.Fatal("generated vhost missing")
	}
	if _, err := os.Stat(filepath.Join(root, ".lightngx/maintenance.html")); err != nil {
		t.Fatal("maintenance page missing")
	}

	// Rollback path: back to the plain enabled state, generated file gone.
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	list, _ = m.List()
	if !list[0].Enabled || list[0].Maintenance {
		t.Fatalf("after rollback: %+v", list)
	}
	if _, err := os.Stat(gen); !os.IsNotExist(err) {
		t.Fatal("generated vhost should be removed on rollback")
	}

	// Full cycle: on, then off.
	if _, err := m.MaintenanceOn("blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MaintenanceOff("blog"); err != nil {
		t.Fatal(err)
	}
	m.Cleanup("blog")
	list, _ = m.List()
	if !list[0].Enabled || list[0].Maintenance {
		t.Fatalf("after maintenance off: %+v", list)
	}
	if _, err := os.Stat(gen); !os.IsNotExist(err) {
		t.Fatal("generated vhost should be cleaned up")
	}
}

func TestRefusesRegularFile(t *testing.T) {
	m, root := setup(t)
	if err := os.WriteFile(filepath.Join(root, "sites-enabled/blog"), []byte("server {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("blog"); err != ErrNotSymlink {
		t.Fatalf("want ErrNotSymlink, got %v", err)
	}
	if _, err := m.MaintenanceOn("blog"); err != ErrNotSymlink {
		t.Fatalf("want ErrNotSymlink, got %v", err)
	}
}

func TestBadNames(t *testing.T) {
	m, _ := setup(t)
	for _, name := range []string{"", "..", "a/b", ".hidden", "x\x00y"} {
		if _, err := m.Enable(name); err == nil {
			t.Errorf("Enable(%q) should fail", name)
		}
	}
}

func TestRename(t *testing.T) {
	m, root := setup(t)
	if _, err := m.Enable("blog"); err != nil {
		t.Fatal(err)
	}
	restore, err := m.Rename("blog", "journal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites-available/journal")); err != nil {
		t.Fatal("renamed file missing")
	}
	if target, err := os.Readlink(filepath.Join(root, "sites-enabled/journal")); err != nil || target != "../sites-available/journal" {
		t.Fatalf("new symlink = %q err=%v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "sites-enabled/blog")); !os.IsNotExist(err) {
		t.Fatal("old symlink still present")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(root, "sites-enabled/blog")); err != nil || target != "../sites-available/blog" {
		t.Fatalf("restored symlink = %q err=%v", target, err)
	}

	// Refuse rename while in maintenance, and to an existing name.
	if _, err := m.MaintenanceOn("blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rename("blog", "x"); err != ErrInMaintenance {
		t.Fatalf("want ErrInMaintenance, got %v", err)
	}
	if _, err := m.MaintenanceOff("blog"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "sites-available/taken"), []byte("server {}"), 0o644)
	if _, err := m.Rename("blog", "taken"); err != ErrExists {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestClone(t *testing.T) {
	m, root := setup(t)
	if _, err := m.Enable("blog"); err != nil {
		t.Fatal(err)
	}
	if err := m.Clone("blog", "blog-copy"); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(root, "sites-available/blog"))
	dst, err := os.ReadFile(filepath.Join(root, "sites-available/blog-copy"))
	if err != nil || string(src) != string(dst) {
		t.Fatalf("clone content mismatch err=%v", err)
	}
	// Clone is disabled: no sites-enabled symlink.
	if _, err := os.Lstat(filepath.Join(root, "sites-enabled/blog-copy")); !os.IsNotExist(err) {
		t.Fatal("clone should not be enabled")
	}
	if list, _ := m.List(); len(list) != 2 {
		t.Fatalf("want 2 sites after clone, got %d", len(list))
	}
	// Refuse an existing target and a missing source.
	if err := m.Clone("blog", "blog-copy"); err != ErrExists {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if err := m.Clone("nope", "x"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestVhostDelete(t *testing.T) {
	m, root := setup(t)
	if _, err := m.Enable("blog"); err != nil {
		t.Fatal(err)
	}
	restore, err := m.Delete("blog")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites-available/blog")); !os.IsNotExist(err) {
		t.Fatal("available file still present")
	}
	if _, err := os.Lstat(filepath.Join(root, "sites-enabled/blog")); !os.IsNotExist(err) {
		t.Fatal("symlink still present")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	list, _ := m.List()
	if len(list) != 1 || !list[0].Enabled {
		t.Fatalf("after restore: %+v", list)
	}
	// Delete while in maintenance removes the generated vhost too.
	if _, err := m.MaintenanceOn("blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete("blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lightngx/maintenance/blog.conf")); !os.IsNotExist(err) {
		t.Fatal("generated maintenance vhost not cleaned up")
	}
}
