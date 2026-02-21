package handler

import (
	"log"
	"net/http"
	"strings"

	"askflow/internal/errlog"
	"askflow/internal/query"
)

// HandleQuery processes a user question through the RAG pipeline.
func HandleQuery(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Validate user session
		_, err := GetUserSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req query.QueryRequest
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		question := strings.TrimSpace(req.Question)
		if question == "" {
			WriteError(w, http.StatusBadRequest, "question is required")
			return
		}
		// Limit question length to prevent abuse
		if len(question) > 10000 {
			WriteError(w, http.StatusBadRequest, "question too long (max 10000 characters)")
			return
		}
		req.Question = question
		// Validate product_id format if provided
		if req.ProductID != "" && !IsValidOptionalID(req.ProductID) {
			WriteError(w, http.StatusBadRequest, "invalid product_id")
			return
		}
		// Default to first product if no product_id specified
		if req.ProductID == "" {
			firstID, pErr := app.GetFirstProductID()
			if pErr == nil && firstID != "" {
				req.ProductID = firstID
			}
		}
		resp, err := app.queryEngine.Query(req)
		if err != nil {
			log.Printf("[Query] error: %v", err)
			errlog.Logf("[Query] query processing failed: %v", err)
			WriteError(w, http.StatusInternalServerError, "查询处理失败，请稍后重试")
			return
		}
		// Strip debug info for non-admin users to prevent information leakage
		if resp.DebugInfo != nil {
			_, _, adminErr := GetAdminSession(app, r)
			if adminErr != nil {
				resp.DebugInfo = nil
			}
		}
		// Check if product allows document download
		if req.ProductID != "" {
			p, pErr := app.GetProduct(req.ProductID)
			if pErr == nil && p != nil {
				resp.AllowDownload = p.AllowDownload
			}
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}
