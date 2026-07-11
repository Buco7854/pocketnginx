// Package nginxctl tests, reloads and restarts nginx. It supports two
// modes: supervising nginx as a child process (containers where
// lightngx is the init), or driving an externally-managed nginx
// through its pidfile.
package nginxctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Controller struct {
	bin     string
	conf    string
	pidFile string

	// op serializes reload, restart and shutdown: without it two
	// concurrent restarts interleave their stop/start pairs and leave an
	// orphaned master fighting the tracked one for the listen sockets.
	op sync.Mutex

	mu         sync.Mutex
	supervise  bool
	logrotate  bool
	child      *exec.Cmd
	childDone  chan struct{}
	stopping   bool
	shutdown   bool
	shutdownCh chan struct{}
}

func New(bin, conf, pidFile string, supervise, logrotate bool) *Controller {
	return &Controller{
		bin:        bin,
		conf:       conf,
		pidFile:    pidFile,
		supervise:  supervise,
		logrotate:  logrotate,
		shutdownCh: make(chan struct{}),
	}
}

// Test runs `nginx -t` and returns its combined output.
func (c *Controller) Test(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "-t", "-c", c.conf)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// Version returns the `nginx -v` banner.
func (c *Controller) Version(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, c.bin, "-v")
	var buf bytes.Buffer
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(buf.String(), "nginx version: "))
}

func (c *Controller) masterPID() (int, error) {
	c.mu.Lock()
	if c.supervise && c.child != nil && c.child.Process != nil {
		pid := c.child.Process.Pid
		c.mu.Unlock()
		return pid, nil
	}
	c.mu.Unlock()
	b, err := os.ReadFile(c.pidFile)
	if err != nil {
		return 0, fmt.Errorf("nginx pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, errors.New("nginx pidfile: bad content")
	}
	return pid, nil
}

// Running reports whether the nginx master process is alive.
func (c *Controller) Running() bool {
	pid, err := c.masterPID()
	if err != nil {
		return false
	}
	return syscall.Kill(pid, 0) == nil || errors.Is(syscall.Kill(pid, 0), syscall.EPERM)
}

// Reload sends SIGHUP to the nginx master after a successful config test.
func (c *Controller) Reload(ctx context.Context) (string, error) {
	c.op.Lock()
	defer c.op.Unlock()
	out, err := c.Test(ctx)
	if err != nil {
		return out, fmt.Errorf("config test failed, reload aborted")
	}
	pid, err := c.masterPID()
	if err != nil {
		return out, err
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return out, fmt.Errorf("signal nginx: %w", err)
	}
	return out, nil
}

// Restart fully stops and starts nginx. In supervise mode the child is
// respawned; in external mode the master gets SIGQUIT and the external
// supervisor is expected to bring it back.
func (c *Controller) Restart(ctx context.Context) (string, error) {
	c.op.Lock()
	defer c.op.Unlock()
	out, err := c.Test(ctx)
	if err != nil {
		return out, fmt.Errorf("config test failed, restart aborted")
	}
	c.mu.Lock()
	supervise := c.supervise
	c.mu.Unlock()
	if supervise {
		if err := c.stopChild(15 * time.Second); err != nil {
			return out, err
		}
		if err := c.startChild(); err != nil {
			return out, err
		}
		return out, nil
	}
	pid, err := c.masterPID()
	if err != nil {
		return out, err
	}
	if err := syscall.Kill(pid, syscall.SIGQUIT); err != nil {
		return out, fmt.Errorf("signal nginx: %w", err)
	}
	return out, nil
}

// StartSupervised launches nginx as a managed child and keeps it running:
// unexpected exits are respawned with backoff so the UI stays available
// to fix a broken config.
func (c *Controller) StartSupervised() error {
	if err := c.startChild(); err != nil {
		return err
	}
	go c.superviseLoop()
	if c.logrotate {
		go c.logrotateLoop()
	}
	return nil
}

// logrotateLoop runs logrotate on a timer (there is no cron in the image).
// It is a no-op when logrotate or its config are absent (e.g. a non-image
// run). logrotate itself only rotates when the policy in the config is due,
// so an hourly check is cheap and also catches size-based rules promptly.
func (c *Controller) logrotateLoop() {
	bin, err := exec.LookPath("logrotate")
	if err != nil {
		return
	}
	const conf = "/etc/logrotate.d/nginx"
	if _, err := os.Stat(conf); err != nil {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-c.shutdownCh:
			return
		case <-t.C:
			cmd := exec.Command(bin, conf)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				slog.Warn("logrotate failed", "error", err)
			}
		}
	}
}

func (c *Controller) startChild() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		// A respawn racing Shutdown would leave an unsupervised nginx
		// running after lightngx exits.
		return errors.New("shutting down")
	}
	cmd := exec.Command(c.bin, "-c", c.conf, "-g", "daemon off;")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start nginx: %w", err)
	}
	done := make(chan struct{})
	c.child = cmd
	c.childDone = done
	go func() {
		err := cmd.Wait()
		if err != nil {
			slog.Warn("nginx exited", "error", err)
		} else {
			slog.Info("nginx exited cleanly")
		}
		close(done)
	}()
	slog.Info("nginx started", "pid", cmd.Process.Pid)
	return nil
}

func (c *Controller) stopChild(timeout time.Duration) error {
	c.mu.Lock()
	child, done := c.child, c.childDone
	c.stopping = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.stopping = false
		c.mu.Unlock()
	}()
	if child == nil || child.Process == nil {
		return nil
	}
	if err := child.Process.Signal(syscall.SIGQUIT); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal nginx: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		_ = child.Process.Kill()
		<-done
		return nil
	}
}

func (c *Controller) superviseLoop() {
	backoff := time.Second
	for {
		c.mu.Lock()
		done := c.childDone
		c.mu.Unlock()
		select {
		case <-c.shutdownCh:
			return
		case <-done:
		}
		c.mu.Lock()
		stopping := c.stopping
		c.mu.Unlock()
		if stopping {
			// Deliberate stop (restart in progress); wait for the new child.
			time.Sleep(200 * time.Millisecond)
			continue
		}
		select {
		case <-c.shutdownCh:
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		c.mu.Lock()
		replaced := c.childDone != done
		c.mu.Unlock()
		if replaced {
			// Restart already spawned a new child while we backed off.
			backoff = time.Second
			continue
		}
		slog.Info("respawning nginx")
		if err := c.startChild(); err != nil {
			slog.Error("respawn failed", "error", err)
		} else {
			backoff = time.Second
		}
	}
}

// Shutdown gracefully stops the supervised nginx (no-op in external mode).
func (c *Controller) Shutdown() {
	c.mu.Lock()
	c.shutdown = true
	supervise := c.supervise
	c.mu.Unlock()
	close(c.shutdownCh)
	if supervise {
		c.op.Lock()
		defer c.op.Unlock()
		_ = c.stopChild(20 * time.Second)
	}
}
