package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"askflow/internal/auth"
)

// --- Admin sub-account handlers ---

// HandleAdminUsers handles listing and creating admin sub-accounts.
func HandleAdminUsers(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}

		switch r.Method {
		case http.MethodGet:
			if role != "super_admin" {
				WriteError(w, http.StatusForbidden, "仅超级管理员可管理用户")
				return
			}
			users, err := app.ListAdminUsers()
			if err != nil {
				log.Printf("[Admin] list users error: %v", err)
				WriteError(w, http.StatusInternalServerError, "获取用户列表失败")
				return
			}
			if users == nil {
				users = []AdminUserInfo{}
			}
			WriteJSON(w, http.StatusOK, map[string]interface{}{"users": users})

		case http.MethodPost:
			if role != "super_admin" {
				WriteError(w, http.StatusForbidden, "仅超级管理员可管理用户")
				return
			}
			var req struct {
				Username    string   `json:"username"`
				Password    string   `json:"password"`
				Role        string   `json:"role"`
				ProductIDs  []string `json:"product_ids"`
				Permissions []string `json:"permissions"`
			}
			if err := ReadJSONBody(r, &req); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			user, err := app.CreateAdminUser(req.Username, req.Password, req.Role, req.Permissions)
			if err != nil {
				WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
			if len(req.ProductIDs) > 0 {
				if err := app.AssignProductsToAdminUser(user.ID, req.ProductIDs); err != nil {
					log.Printf("[Admin] assign products error: %v", err)
					WriteError(w, http.StatusInternalServerError, "分配产品失败")
					return
				}
			}
			WriteJSON(w, http.StatusOK, user)

		default:
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// HandleAdminUserByID handles deleting an admin sub-account by ID.
func HandleAdminUserByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "仅超级管理员可管理用户")
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
		if id == "" {
			WriteError(w, http.StatusBadRequest, "missing user ID")
			return
		}
		// Validate ID format
		if !IsValidHexID(id) {
			WriteError(w, http.StatusBadRequest, "invalid user ID")
			return
		}

		if r.Method != http.MethodDelete {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if err := app.DeleteAdminUser(id); err != nil {
			log.Printf("[Admin] delete user error for %s: %v", id, err)
			WriteError(w, http.StatusInternalServerError, "删除用户失败")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleAdminRole returns the current admin user's role and permissions.
func HandleAdminRole(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		userID, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteJSON(w, http.StatusOK, map[string]interface{}{"role": "", "permissions": []string{}})
			return
		}
		perms := app.GetAdminPermissions(userID)
		if perms == nil {
			perms = []string{}
		}
		WriteJSON(w, http.StatusOK, map[string]interface{}{"role": role, "permissions": perms})
	}
}

// --- Login ban management handlers ---

// HandleAdminBans returns the list of current login bans.
func HandleAdminBans(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "仅超级管理员可管理登录限制")
			return
		}
		bans := app.loginLimiter.ListBans()
		if bans == nil {
			bans = []auth.BanEntry{}
		}
		WriteJSON(w, http.StatusOK, map[string]interface{}{"bans": bans})
	}
}

// HandleAdminUnban removes a login ban for a username or IP.
func HandleAdminUnban(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "仅超级管理员可管理登录限制")
			return
		}
		var req struct {
			Username string `json:"username"`
			IP       string `json:"ip"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		app.loginLimiter.Unban(req.Username, req.IP)
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleAdminAddBan manually bans a username or IP.
func HandleAdminAddBan(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "仅超级管理员可管理登录限制")
			return
		}
		var req struct {
			Username string `json:"username"`
			IP       string `json:"ip"`
			Reason   string `json:"reason"`
			Days     int    `json:"days"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Username == "" && req.IP == "" {
			WriteError(w, http.StatusBadRequest, "请输入用户名或IP")
			return
		}
		if req.Days <= 0 {
			req.Days = 1
		}
		if req.Reason == "" {
			req.Reason = "管理员手动封禁"
		}
		app.loginLimiter.AddManualBan(req.Username, req.IP, req.Reason, time.Duration(req.Days)*24*time.Hour)
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// --- Customer management handlers ---

// HandleAdminCustomers returns a paginated list of customer accounts.
func HandleAdminCustomers(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "insufficient permissions")
			return
		}

		// Parse pagination and search params
		page := 1
		pageSize := 20
		search := r.URL.Query().Get("search")
		if p := r.URL.Query().Get("page"); p != "" {
			if v, e := strconv.Atoi(p); e == nil && v > 0 {
				page = v
			}
		}
		if ps := r.URL.Query().Get("page_size"); ps != "" {
			if v, e := strconv.Atoi(ps); e == nil && v > 0 {
				pageSize = v
			}
		}
		if pageSize > 200 {
			pageSize = 200
		}

		result, err := app.ListCustomersPaged(page, pageSize, search)
		if err != nil {
			log.Printf("[Admin] list customers error: %v", err)
			WriteError(w, http.StatusInternalServerError, "failed to list customers")
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

// HandleAdminCustomerVerify manually verifies a customer's email.
func HandleAdminCustomerVerify(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil || role != "super_admin" {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			UserID string `json:"user_id"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.UserID == "" || len(req.UserID) > 128 {
			WriteError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		if err := app.VerifyCustomerEmail(req.UserID); err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleAdminCustomerBan bans a customer by email.
func HandleAdminCustomerBan(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil || role != "super_admin" {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			Email  string `json:"email"`
			Reason string `json:"reason"`
			Days   int    `json:"days"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Email == "" || len(req.Email) > 254 {
			WriteError(w, http.StatusBadRequest, "invalid email")
			return
		}
		if err := app.BanCustomer(req.Email, req.Reason, req.Days); err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleAdminCustomerUnban removes a ban on a customer by email.
func HandleAdminCustomerUnban(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil || role != "super_admin" {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			Email string `json:"email"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.UnbanCustomer(req.Email); err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleAdminCustomerDelete removes a customer account.
func HandleAdminCustomerDelete(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil || role != "super_admin" {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			UserID string `json:"user_id"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.UserID == "" || len(req.UserID) > 128 {
			WriteError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		if err := app.DeleteCustomer(req.UserID); err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
