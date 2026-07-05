package server

import (
	"net/http"
	"strings"

	"mudp/internal/store"
)

// registries handles admin CRUD for stored registry credentials.
func (a *App) registries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := a.db.Registries()
		// Strip tokens from the list response — write-only secrets.
		for i := range items {
			items[i].Token = ""
		}
		respond(w, items, err)
	case http.MethodPost:
		a.registryMu.Lock()
		defer a.registryMu.Unlock()
		var req struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			URL      string `json:"url"`
			Username string `json:"username"`
			Token    string `json:"token"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.URL = strings.TrimSpace(req.URL)
		if req.Name == "" || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "name and url are required")
			return
		}
		items, err := a.db.Registries()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if req.ID == 0 {
			// Create: token required so the credential is usable.
			if req.Token == "" {
				writeErr(w, http.StatusBadRequest, "token is required when adding a registry")
				return
			}
			req.ID = nextRegistryID(items)
			items = append(items, store.Registry{
				ID: req.ID, Name: req.Name, URL: req.URL, Username: req.Username, Token: req.Token,
			})
		} else {
			// Update: blank token means keep the existing one.
			updated := false
			for i, it := range items {
				if it.ID == req.ID {
					items[i].Name = req.Name
					items[i].URL = req.URL
					items[i].Username = req.Username
					if req.Token != "" {
						items[i].Token = req.Token
					}
					updated = true
					break
				}
			}
			if !updated {
				writeErr(w, http.StatusNotFound, "registry not found")
				return
			}
		}
		if err := a.db.SaveRegistries(items); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.record(r, "registry.save", req.Name)
		writeJSON(w, http.StatusOK, map[string]int64{"id": req.ID})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// registryDelete removes a stored registry.
func (a *App) registryDelete(w http.ResponseWriter, r *http.Request) {
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	items, err := a.db.Registries()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := items[:0]
	var name string
	for _, it := range items {
		if it.ID == req.ID {
			name = it.Name
			continue
		}
		out = append(out, it)
	}
	if err := a.db.SaveRegistries(out); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.record(r, "registry.delete", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// registryTest verifies a stored credential by performing a registry login.
func (a *App) registryTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == 0 {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	items, _ := a.db.Registries()
	var target store.Registry
	for _, it := range items {
		if it.ID == req.ID {
			target = it
			break
		}
	}
	if target.URL == "" {
		writeErr(w, http.StatusNotFound, "registry not found")
		return
	}
	if err := a.docker.Login(r.Context(), target.URL, target.Username, target.Token); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// nextRegistryID returns max(id)+1 for new registry entries.
func nextRegistryID(items []store.Registry) int64 {
	var max int64
	for _, it := range items {
		if it.ID > max {
			max = it.ID
		}
	}
	return max + 1
}
