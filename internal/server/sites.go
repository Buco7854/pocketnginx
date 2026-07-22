package server

import (
	"errors"
	"net/http"

	"github.com/Buco7854/lightngx/internal/sites"
)

// vhostKind resolves the manager + capabilities for a sites/streams
// endpoint from its kind segment.
func (s *Server) vhostKind(kind string) (mgr *sites.Manager, maintenance bool) {
	switch kind {
	case "sites":
		return s.sites, s.cfg.MaintenanceEnabled
	case "streams":
		return s.streams, false
	default:
		return nil, false
	}
}

func (s *Server) handleVhostList(w http.ResponseWriter, r *http.Request) {
	mgr, maintenance := s.vhostKind(r.PathValue("kind"))
	resp := map[string]any{
		"available":   mgr != nil && mgr.Ready(),
		"maintenance": maintenance,
		"sites":       []sites.Site{},
	}
	if mgr != nil && mgr.Ready() {
		list, err := mgr.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if list != nil {
			resp["sites"] = list
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVhostAction applies one action to one or more vhosts, guards the
// whole batch with a single `nginx -t` (rolling everything back on
// failure) and reloads nginx once on success.
func (s *Server) handleVhostAction(w http.ResponseWriter, r *http.Request) {
	mgr, maintenanceOK := s.vhostKind(r.PathValue("kind"))
	if mgr == nil || !mgr.Ready() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not available"})
		return
	}
	var req struct {
		Name   string   `json:"name"`
		Names  []string `json:"names"`
		Action string   `json:"action"`
	}
	if !readJSON(w, r, &req, 64<<10) {
		return
	}
	names := req.Names
	if len(names) == 0 && req.Name != "" {
		names = []string{req.Name}
	}
	if len(names) == 0 || len(names) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no names given"})
		return
	}
	if (req.Action == "maintenance_on" || req.Action == "maintenance_off") && !maintenanceOK {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "maintenance mode not available"})
		return
	}

	s.mutate.Lock()
	defer s.mutate.Unlock()
	var restores []func() error
	rollback := func() bool {
		ok := true
		for i := len(restores) - 1; i >= 0; i-- {
			if err := restores[i](); err != nil {
				ok = false
			}
		}
		return ok
	}

	cleanup := req.Action == "disable" || req.Action == "maintenance_off" || req.Action == "delete"
	for _, name := range names {
		var restore func() error
		var err error
		switch req.Action {
		case "enable":
			restore, err = mgr.Enable(name)
		case "disable":
			restore, err = mgr.Disable(name)
		case "maintenance_on":
			restore, err = mgr.MaintenanceOn(name)
		case "maintenance_off":
			restore, err = mgr.MaintenanceOff(name)
		case "delete":
			restore, err = mgr.Delete(name)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			return
		}
		if err != nil {
			rollback()
			writeJSON(w, siteStatusFor(err), map[string]string{"error": name + ": " + err.Error()})
			return
		}
		restores = append(restores, restore)
	}

	sess, _ := sessionFrom(r.Context())
	out, err := s.nginx.Test(r.Context())
	if err != nil {
		if !rollback() {
			s.audit(r, "vhost."+req.Action+".rollback_failed", "by", sess.User, "names", names)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "config test failed AND rollback failed, fix manually", "output": out})
			return
		}
		s.audit(r, "vhost."+req.Action+".rejected", "names", names)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "nginx config test failed, change rolled back", "output": out})
		return
	}
	if cleanup {
		for _, name := range names {
			mgr.Cleanup(name)
		}
	}
	if _, err := s.nginx.Reload(r.Context()); err != nil {
		s.audit(r, "vhost."+req.Action+".reload_failed", "names", names, "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "change applied but nginx reload failed", "output": out})
		return
	}
	s.audit(r, "vhost."+req.Action, "names", names)
	writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "output": out, "reloaded": true})
}

// handleVhostRename renames a vhost, `nginx -t` guarded, then reloads.
func (s *Server) handleVhostRename(w http.ResponseWriter, r *http.Request) {
	mgr, _ := s.vhostKind(r.PathValue("kind"))
	if mgr == nil || !mgr.Ready() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not available"})
		return
	}
	var req struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	s.mutate.Lock()
	defer s.mutate.Unlock()
	restore, err := mgr.Rename(req.Name, req.NewName)
	if err != nil {
		writeJSON(w, siteStatusFor(err), map[string]string{"error": err.Error()})
		return
	}
	out, err := s.nginx.Test(r.Context())
	if err != nil {
		if rerr := restore(); rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "config test failed AND rollback failed, fix manually", "output": out})
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "nginx config test failed, rename rolled back", "output": out})
		return
	}
	if _, err := s.nginx.Reload(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "renamed but nginx reload failed", "output": out})
		return
	}
	s.audit(r, "vhost.renamed", "from", req.Name, "to", req.NewName)
	writeJSON(w, http.StatusOK, map[string]any{"status": "renamed", "output": out, "reloaded": true})
}

// handleVhostClone copies a vhost to a new name. The copy is disabled, so
// it does not touch the running config and needs no test or reload.
func (s *Server) handleVhostClone(w http.ResponseWriter, r *http.Request) {
	mgr, _ := s.vhostKind(r.PathValue("kind"))
	if mgr == nil || !mgr.Ready() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not available"})
		return
	}
	var req struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	s.mutate.Lock()
	defer s.mutate.Unlock()
	if err := mgr.Clone(req.Name, req.NewName); err != nil {
		writeJSON(w, siteStatusFor(err), map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "vhost.cloned", "from", req.Name, "to", req.NewName)
	writeJSON(w, http.StatusOK, map[string]any{"status": "cloned"})
}

func siteStatusFor(err error) int {
	switch {
	case errors.Is(err, sites.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, sites.ErrBadName):
		return http.StatusBadRequest
	case errors.Is(err, sites.ErrNotSymlink), errors.Is(err, sites.ErrExists),
		errors.Is(err, sites.ErrInMaintenance):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
