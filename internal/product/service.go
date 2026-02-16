// Package product provides product management for the askflow system.
package product

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Product represents a product entity in the system.
// Type can be "service" (产品服务, requires intent classification) or "knowledge_base" (知识库, no intent filtering).
type Product struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Description    string    `json:"description"`
	WelcomeMessage string    `json:"welcome_message"`
	AllowDownload  bool      `json:"allow_download"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const (
	ProductTypeService       = "service"
	ProductTypeKnowledgeBase = "knowledge_base"
)


// ProductService handles CRUD operations for products.
type ProductService struct {
	db *sql.DB
}

// NewProductService creates a new ProductService with the given database connection.
func NewProductService(db *sql.DB) *ProductService {
	return &ProductService{db: db}
}

// Create creates a new product with the given name, description, and welcome message.
// Returns an error if the name is empty or already exists.
func (s *ProductService) Create(name, productType, description, welcomeMessage string, allowDownload bool) (*Product, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("product name cannot be empty")
	}
	if len(name) > 200 {
		return nil, fmt.Errorf("product name too long (max 200 characters)")
	}
	if len(description) > 5000 {
		return nil, fmt.Errorf("description too long (max 5000 characters)")
	}
	if len(welcomeMessage) > 10000 {
		return nil, fmt.Errorf("welcome message too long (max 10000 characters)")
	}

	// Validate product type
	if productType != ProductTypeService && productType != ProductTypeKnowledgeBase {
		productType = ProductTypeService // default to service
	}

	// Check uniqueness
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM products WHERE name = ?", name).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("failed to check product name uniqueness: %w", err)
	}
	if count > 0 {
		return nil, fmt.Errorf("product name already exists")
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	_, err = s.db.Exec(
		"INSERT INTO products (id, name, type, description, welcome_message, allow_download, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		id, name, productType, description, welcomeMessage, allowDownload, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create product: %w", err)
	}

	return &Product{
		ID:             id,
		Name:           name,
		Type:           productType,
		Description:    description,
		WelcomeMessage: welcomeMessage,
		AllowDownload:  allowDownload,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Update updates an existing product's name, description, and welcome message.
// Returns an error if the name is empty or already used by another product.
func (s *ProductService) Update(id, name, productType, description, welcomeMessage string, allowDownload bool) (*Product, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("product name cannot be empty")
	}
	if len(name) > 200 {
		return nil, fmt.Errorf("product name too long (max 200 characters)")
	}
	if len(description) > 5000 {
		return nil, fmt.Errorf("description too long (max 5000 characters)")
	}
	if len(welcomeMessage) > 10000 {
		return nil, fmt.Errorf("welcome message too long (max 10000 characters)")
	}

	// Validate product type
	if productType != ProductTypeService && productType != ProductTypeKnowledgeBase {
		productType = ProductTypeService
	}

	// Check uniqueness excluding self
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM products WHERE name = ? AND id != ?", name, id).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("failed to check product name uniqueness: %w", err)
	}
	if count > 0 {
		return nil, fmt.Errorf("product name already exists")
	}

	now := time.Now()
	result, err := s.db.Exec(
		"UPDATE products SET name = ?, type = ?, description = ?, welcome_message = ?, allow_download = ?, updated_at = ? WHERE id = ?",
		name, productType, description, welcomeMessage, allowDownload, now, id,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update product: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to check update result: %w", err)
	}
	if rows == 0 {
		return nil, fmt.Errorf("product not found")
	}

	return s.GetByID(id)
}

// Delete removes a product and disassociates all related documents and chunks.
// Uses a transaction to ensure atomicity.
func (s *ProductService) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Set product_id to empty string on associated documents
	if _, err := tx.Exec("UPDATE documents SET product_id = '' WHERE product_id = ?", id); err != nil {
		return fmt.Errorf("failed to disassociate documents: %w", err)
	}

	// Set product_id to empty string on associated chunks
	if _, err := tx.Exec("UPDATE chunks SET product_id = '' WHERE product_id = ?", id); err != nil {
		return fmt.Errorf("failed to disassociate chunks: %w", err)
	}

	// Delete admin user product assignments
	if _, err := tx.Exec("DELETE FROM admin_user_products WHERE product_id = ?", id); err != nil {
		return fmt.Errorf("failed to delete admin user product assignments: %w", err)
	}

	// Delete the product record
	result, err := tx.Exec("DELETE FROM products WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete product: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check delete result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("product not found")
	}

	return tx.Commit()
}

// GetByID returns a product by its ID.
func (s *ProductService) GetByID(id string) (*Product, error) {
	var p Product
	var allowDL int
	err := s.db.QueryRow(
		"SELECT id, name, COALESCE(type, 'service'), description, welcome_message, COALESCE(allow_download, 0), created_at, updated_at FROM products WHERE id = ?", id,
	).Scan(&p.ID, &p.Name, &p.Type, &p.Description, &p.WelcomeMessage, &allowDL, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("product not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get product: %w", err)
	}
	p.AllowDownload = allowDL == 1
	return &p, nil
}

// List returns all products ordered by created_at.
func (s *ProductService) List() ([]Product, error) {
	rows, err := s.db.Query("SELECT id, name, COALESCE(type, 'service'), description, welcome_message, COALESCE(allow_download, 0), created_at, updated_at FROM products ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("failed to list products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		var allowDL int
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Description, &p.WelcomeMessage, &allowDL, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		p.AllowDownload = allowDL == 1
		products = append(products, p)
	}
	return products, rows.Err()
}

// HasDocumentsOrKnowledge checks if any documents or chunks are associated with the given product ID.
func (s *ProductService) HasDocumentsOrKnowledge(productID string) (bool, error) {
	var docCount int
	err := s.db.QueryRow("SELECT COUNT(*) FROM documents WHERE product_id = ?", productID).Scan(&docCount)
	if err != nil {
		return false, fmt.Errorf("failed to count documents: %w", err)
	}
	if docCount > 0 {
		return true, nil
	}

	var chunkCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM chunks WHERE product_id = ?", productID).Scan(&chunkCount)
	if err != nil {
		return false, fmt.Errorf("failed to count chunks: %w", err)
	}
	return chunkCount > 0, nil
}

// HasProducts returns true if at least one product exists.
func (s *ProductService) HasProducts() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM products").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to count products: %w", err)
	}
	return count > 0, nil
}

// AssignAdminUser assigns a set of products to an admin user.
// It replaces all existing product assignments for the given admin user.
// If productIDs is empty, all existing assignments are removed (admin gets access to all products).
func (s *ProductService) AssignAdminUser(adminUserID string, productIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete all existing assignments for this admin user
	if _, err := tx.Exec("DELETE FROM admin_user_products WHERE admin_user_id = ?", adminUserID); err != nil {
		return fmt.Errorf("failed to delete existing product assignments: %w", err)
	}

	// Insert new assignments
	for _, productID := range productIDs {
		if _, err := tx.Exec(
			"INSERT INTO admin_user_products (admin_user_id, product_id) VALUES (?, ?)",
			adminUserID, productID,
		); err != nil {
			return fmt.Errorf("failed to assign product %s: %w", productID, err)
		}
	}

	return tx.Commit()
}

// GetByAdminUserID returns the products assigned to an admin user.
// If no products are assigned, returns all products (per Requirement 2.4).
func (s *ProductService) GetByAdminUserID(adminUserID string) ([]Product, error) {
	// Check how many products are assigned
	rows, err := s.db.Query("SELECT product_id FROM admin_user_products WHERE admin_user_id = ?", adminUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to query admin user products: %w", err)
	}
	defer rows.Close()

	var productIDs []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, fmt.Errorf("failed to scan product id: %w", err)
		}
		productIDs = append(productIDs, pid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate product ids: %w", err)
	}

	// Zero assignments means access to all products
	if len(productIDs) == 0 {
		return s.List()
	}

	// Build query for assigned products
	placeholders := make([]string, len(productIDs))
	args := make([]interface{}, len(productIDs))
	for i, id := range productIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		"SELECT id, name, COALESCE(type, 'service'), description, welcome_message, COALESCE(allow_download, 0), created_at, updated_at FROM products WHERE id IN (%s) ORDER BY created_at",
		strings.Join(placeholders, ", "),
	)

	productRows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query products: %w", err)
	}
	defer productRows.Close()

	var products []Product
	for productRows.Next() {
		var p Product
		var allowDL int
		if err := productRows.Scan(&p.ID, &p.Name, &p.Type, &p.Description, &p.WelcomeMessage, &allowDL, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		p.AllowDownload = allowDL == 1
		products = append(products, p)
	}
	return products, productRows.Err()
}


// generateID creates a random hex string for use as a unique identifier.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
