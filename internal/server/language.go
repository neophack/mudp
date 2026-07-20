package server

import (
	"net/http"
	"strconv"

	"mudp/internal/store"
)

const (
	LanguageZhCN = "zh_CN"
	LanguageEnUS = "en_US"
)

// ValidLanguage checks if the given language is supported
func ValidLanguage(lang string) bool {
	switch lang {
	case LanguageZhCN, LanguageEnUS:
		return true
	}
	return false
}

// userLanguage handles GET/POST for user's language preference.
// GET: returns the current user's language preference and system default
// POST: updates the current user's language preference
func (a *App) userLanguage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.getUserLanguage(w, r, u)
	case http.MethodPost:
		a.setUserLanguage(w, r, u)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getUserLanguage returns the current user's language and the system default
func (a *App) getUserLanguage(w http.ResponseWriter, r *http.Request, u *store.User) {
	resp := struct {
		UserLanguage    string `json:"userLanguage"`
		DefaultLanguage string `json:"defaultLanguage"`
	}{
		UserLanguage:    u.Language,
		DefaultLanguage: a.cfg.DefaultLanguage,
	}
	writeJSON(w, http.StatusOK, resp)
}

// setUserLanguage updates the current user's language preference
func (a *App) setUserLanguage(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Language string `json:"language"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Language == "" {
		writeErr(w, http.StatusBadRequest, "language is required")
		return
	}

	if !ValidLanguage(req.Language) {
		writeErr(w, http.StatusBadRequest, "unsupported language")
		return
	}

	// Update user's language preference
	err := a.db.UpdateUserLanguage(u.ID, req.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to update language")
		return
	}

	a.record(r, "set_language", req.Language)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Language updated",
	})
}

// adminLanguageSettings handles GET/POST for admin language settings.
// GET: returns the current default language
// POST: updates the system default language
func (a *App) adminLanguageSettings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || u.Role != store.RoleAdmin {
		writeErr(w, http.StatusForbidden, "admin access required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.getAdminLanguageSettings(w, r, u)
	case http.MethodPost:
		a.setAdminLanguageSettings(w, r, u)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getAdminLanguageSettings returns the current system default language
func (a *App) getAdminLanguageSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	resp := struct {
		DefaultLanguage string `json:"defaultLanguage"`
	}{
		DefaultLanguage: a.cfg.DefaultLanguage,
	}
	writeJSON(w, http.StatusOK, resp)
}

// setAdminLanguageSettings updates the system default language
func (a *App) setAdminLanguageSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		DefaultLanguage string `json:"defaultLanguage"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.DefaultLanguage == "" {
		writeErr(w, http.StatusBadRequest, "defaultLanguage is required")
		return
	}

	if !ValidLanguage(req.DefaultLanguage) {
		writeErr(w, http.StatusBadRequest, "unsupported language")
		return
	}

	// Update the config's default language
	a.cfg.DefaultLanguage = req.DefaultLanguage

	// Update all users who don't have a language preference set
	err := a.db.UpdateDefaultLanguageForNewUsers(req.DefaultLanguage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to update default language")
		return
	}

	a.record(r, "set_default_language", req.DefaultLanguage)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "Default language updated",
		"language": req.DefaultLanguage,
	})
}

// groupLanguage handles GET/POST for a group's default language preference.
// GET: returns the group's language preference
// POST: updates the group's language preference
func (a *App) groupLanguage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil || u.Role != store.RoleAdmin {
		writeErr(w, http.StatusForbidden, "admin access required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.getGroupLanguageHandler(w, r, u)
	case http.MethodPost:
		a.setGroupLanguage(w, r, u)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getGroupLanguageHandler returns a group's language preference
func (a *App) getGroupLanguageHandler(w http.ResponseWriter, r *http.Request, u *store.User) {
	groupIDStr := r.URL.Query().Get("id")
	if groupIDStr == "" {
		writeErr(w, http.StatusBadRequest, "group id is required")
		return
	}

	groupID, err := parseInt(groupIDStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid group id")
		return
	}

	groups, err := a.db.Groups()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to fetch groups")
		return
	}

	for _, g := range groups {
		if g.ID == groupID {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"id":       g.ID,
				"name":     g.Name,
				"language": g.Language,
			})
			return
		}
	}

	writeErr(w, http.StatusNotFound, "group not found")
}

// setGroupLanguage updates a group's language preference
func (a *App) setGroupLanguage(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		GroupID  int64  `json:"groupId"`
		Language string `json:"language"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.GroupID == 0 {
		writeErr(w, http.StatusBadRequest, "groupId is required")
		return
	}

	if req.Language == "" {
		writeErr(w, http.StatusBadRequest, "language is required")
		return
	}

	if !ValidLanguage(req.Language) {
		writeErr(w, http.StatusBadRequest, "unsupported language")
		return
	}

	// Update group's language preference
	err := a.db.UpdateGroupLanguage(req.GroupID, req.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to update group language")
		return
	}

	a.record(r, "set_group_language", req.Language)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "Group language updated",
		"language": req.Language,
	})
}

// parseInt parses a string to int64, returning 0 on error
func parseInt(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
