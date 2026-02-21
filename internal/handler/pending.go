package handler

import (
	"log"
	"net/http"
	"strings"

	"askflow/internal/pending"
)

// --- Pending question handlers ---

// HandlePending handles listing pending questions (admin only).
func HandlePending(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session for pending questions listing
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		status := r.URL.Query().Get("status")
		// Validate status parameter
		if status != "" && status != "pending" && status != "answered" && status != "rejected" {
			WriteError(w, http.StatusBadRequest, "invalid status parameter")
			return
		}
		productID := r.URL.Query().Get("product_id")
		if !IsValidOptionalID(productID) {
			WriteError(w, http.StatusBadRequest, "invalid product_id")
			return
		}
		questions, err := app.ListPendingQuestions(status, productID)
		if err != nil {
			log.Printf("[Pending] list error: %v", err)
			WriteError(w, http.StatusInternalServerError, "获取问题列表失败")
			return
		}
		if questions == nil {
			questions = []pending.PendingQuestion{}
		}
		WriteJSON(w, http.StatusOK, map[string]interface{}{"questions": questions})
	}
}

// HandlePendingAnswer handles admin answering a pending question.
func HandlePendingAnswer(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		var req pending.AdminAnswerRequest
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.AnswerQuestion(req); err != nil {
			log.Printf("[Pending] answer error: %v", err)
			WriteError(w, http.StatusInternalServerError, "回答问题失败")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandlePendingCreate handles user creating a new pending question.
func HandlePendingCreate(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Validate user session �?use the authenticated user ID, not the client-provided one
		authenticatedUserID, err := GetUserSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req struct {
			Question  string `json:"question"`
			ImageData string `json:"image_data,omitempty"`
			ProductID string `json:"product_id"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Question == "" {
			WriteError(w, http.StatusBadRequest, "question is required")
			return
		}
		// Limit question length to prevent abuse
		if len(req.Question) > 10000 {
			WriteError(w, http.StatusBadRequest, "question too long")
			return
		}
		// Limit image data size (base64 encoded, ~4MB decoded)
		if len(req.ImageData) > 5*1024*1024 {
			WriteError(w, http.StatusBadRequest, "image data too large")
			return
		}
		pq, err := app.CreatePendingQuestion(req.Question, authenticatedUserID, req.ImageData, req.ProductID)
		if err != nil {
			log.Printf("[Pending] create error: %v", err)
			WriteError(w, http.StatusInternalServerError, "创建问题失败")
			return
		}
		WriteJSON(w, http.StatusOK, pq)
	}
}

// HandlePendingByID handles deleting a pending question by ID (admin only).
func HandlePendingByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/pending/")
		if id == "" || id == "answer" || id == "create" {
			WriteError(w, http.StatusBadRequest, "missing question ID")
			return
		}
		// Validate ID format (hex string only)
		if !IsValidHexID(id) {
			WriteError(w, http.StatusBadRequest, "invalid question ID")
			return
		}
		if r.Method != http.MethodDelete {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		if err := app.DeletePendingQuestion(id); err != nil {
			log.Printf("[Pending] delete error for %s: %v", id, err)
			WriteError(w, http.StatusInternalServerError, "删除问题失败")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}
