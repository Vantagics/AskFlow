package product

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/quick"

	_ "github.com/mattn/go-sqlite3"
)

// counter ensures unique DB names across tests (in-memory DBs need unique names for separate connections).
var dbCounter atomic.Int64

// setupTestDB creates an in-memory SQLite database with all required tables for testing.
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	n := dbCounter.Add(1)
	dsn := fmt.Sprintf("file:testdb%d?mode=memory&cache=shared", n)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.SetMaxOpenConns(1)

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	tables := []string{
		`CREATE TABLE IF NOT EXISTS products (
			id              TEXT PRIMARY KEY,
			name            TEXT NOT NULL UNIQUE,
			type            TEXT DEFAULT 'service',
			description     TEXT DEFAULT '',
			welcome_message TEXT DEFAULT '',
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS admin_users (
			id            TEXT PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'editor',
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS admin_user_products (
			admin_user_id TEXT NOT NULL,
			product_id    TEXT NOT NULL,
			PRIMARY KEY (admin_user_id, product_id),
			FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE,
			FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			type         TEXT NOT NULL,
			status       TEXT NOT NULL,
			error        TEXT,
			content_hash TEXT DEFAULT '',
			product_id   TEXT DEFAULT '',
			created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id            TEXT PRIMARY KEY,
			document_id   TEXT NOT NULL,
			document_name TEXT NOT NULL,
			chunk_index   INTEGER NOT NULL,
			chunk_text    TEXT NOT NULL,
			embedding     BLOB NOT NULL,
			image_url     TEXT DEFAULT '',
			product_id    TEXT DEFAULT '',
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (document_id) REFERENCES documents(id)
		)`,
	}

	for _, ddl := range tables {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	return db, func() { db.Close() }
}

// sanitizeName produces a valid non-empty product name from an arbitrary string.
// It trims whitespace and prepends a prefix to guarantee non-emptiness.
func sanitizeName(s string, suffix int) string {
	// Trim to avoid excessively long names that could cause issues
	if len(s) > 50 {
		s = s[:50]
	}
	return fmt.Sprintf("p%d_%s", suffix, s)
}

// TestProperty1_CRUDRoundTrip verifies that creating and updating a product
// preserves all field values when queried back.
//
// **Feature: multi-product-support, Property 1: 产品 CRUD 往返一致性**
// **Validates: Requirements 1.2, 1.3, 9.1**
func TestProperty1_CRUDRoundTrip(t *testing.T) {
	counter := 0
	f := func(nameSeed string, desc string, welcome string, newNameSeed string, newDesc string, newWelcome string) bool {
		db, cleanup := setupTestDB(t)
		defer cleanup()
		svc := NewProductService(db)

		counter++
		name := sanitizeName(nameSeed, counter)
		newName := sanitizeName(newNameSeed, counter+10000)

		// Create
		created, err := svc.Create(name, "service", desc, welcome)
		if err != nil {
			t.Logf("Create failed: %v", err)
			return false
		}

		// Read back and verify
		got, err := svc.GetByID(created.ID)
		if err != nil {
			t.Logf("GetByID after create failed: %v", err)
			return false
		}
		if got.Name != name || got.Description != desc || got.WelcomeMessage != welcome {
			t.Logf("Create round-trip mismatch: got name=%q desc=%q welcome=%q, want name=%q desc=%q welcome=%q",
				got.Name, got.Description, got.WelcomeMessage, name, desc, welcome)
			return false
		}

		// Update
		updated, err := svc.Update(created.ID, newName, "service", newDesc, newWelcome)
		if err != nil {
			t.Logf("Update failed: %v", err)
			return false
		}

		// Read back and verify update
		got2, err := svc.GetByID(updated.ID)
		if err != nil {
			t.Logf("GetByID after update failed: %v", err)
			return false
		}
		if got2.Name != newName || got2.Description != newDesc || got2.WelcomeMessage != newWelcome {
			t.Logf("Update round-trip mismatch: got name=%q desc=%q welcome=%q, want name=%q desc=%q welcome=%q",
				got2.Name, got2.Description, got2.WelcomeMessage, newName, newDesc, newWelcome)
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestProperty2_NameUniquenessAndNonEmpty verifies that duplicate product names
// are rejected and empty/whitespace-only names are rejected.
//
// **Feature: multi-product-support, Property 2: 产品名称唯一性与非空验证**
// **Validates: Requirements 1.6**
func TestProperty2_NameUniquenessAndNonEmpty(t *testing.T) {
	t.Run("duplicate_names_rejected", func(t *testing.T) {
		counter := 0
		f := func(nameSeed string, desc1 string, desc2 string) bool {
			db, cleanup := setupTestDB(t)
			defer cleanup()
			svc := NewProductService(db)

			counter++
			name := sanitizeName(nameSeed, counter)

			// First creation should succeed
			_, err := svc.Create(name, "service", desc1, "")
			if err != nil {
				t.Logf("First create failed unexpectedly: %v", err)
				return false
			}

			// Second creation with same name should fail
			_, err = svc.Create(name, "service", desc2, "")
			if err == nil {
				t.Logf("Second create with duplicate name %q should have failed", name)
				return false
			}

			return true
		}

		if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
			t.Error(err)
		}
	})

	t.Run("empty_and_whitespace_names_rejected", func(t *testing.T) {
		f := func(spaces uint8) bool {
			db, cleanup := setupTestDB(t)
			defer cleanup()
			svc := NewProductService(db)

			// Empty string
			_, err := svc.Create("", "service", "desc", "")
			if err == nil {
				t.Log("Create with empty name should have failed")
				return false
			}

			// Whitespace-only string (1 to 10 spaces)
			n := int(spaces)%10 + 1
			ws := strings.Repeat(" ", n)
			_, err = svc.Create(ws, "service", "desc", "")
			if err == nil {
				t.Logf("Create with whitespace-only name %q should have failed", ws)
				return false
			}

			// Tabs and mixed whitespace
			_, err = svc.Create("\t \n", "service", "desc", "")
			if err == nil {
				t.Log("Create with tab/newline name should have failed")
				return false
			}

			return true
		}

		if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
			t.Error(err)
		}
	})
}

// TestProperty3_DeleteCascade verifies that deleting a product disassociates
// related documents (setting their product_id to empty string) and removes the product.
//
// **Feature: multi-product-support, Property 3: 产品删除级联**
// **Validates: Requirements 1.4**
func TestProperty3_DeleteCascade(t *testing.T) {
	counter := 0
	f := func(nameSeed string, docName string) bool {
		db, cleanup := setupTestDB(t)
		defer cleanup()
		svc := NewProductService(db)

		counter++
		name := sanitizeName(nameSeed, counter)

		// Ensure docName is non-empty for the document
		if docName == "" {
			docName = "testdoc"
		}

		// Create a product
		p, err := svc.Create(name, "service", "desc", "")
		if err != nil {
			t.Logf("Create failed: %v", err)
			return false
		}

		// Insert a document associated with this product
		docID := fmt.Sprintf("doc_%d", counter)
		_, err = db.Exec(
			"INSERT INTO documents (id, name, type, status, product_id) VALUES (?, ?, 'file', 'ready', ?)",
			docID, docName, p.ID,
		)
		if err != nil {
			t.Logf("Insert document failed: %v", err)
			return false
		}

		// Insert a chunk associated with this product
		chunkID := fmt.Sprintf("chunk_%d", counter)
		_, err = db.Exec(
			"INSERT INTO chunks (id, document_id, document_name, chunk_index, chunk_text, embedding, product_id) VALUES (?, ?, ?, 0, 'text', X'00', ?)",
			chunkID, docID, docName, p.ID,
		)
		if err != nil {
			t.Logf("Insert chunk failed: %v", err)
			return false
		}

		// Delete the product
		err = svc.Delete(p.ID)
		if err != nil {
			t.Logf("Delete failed: %v", err)
			return false
		}

		// Product should no longer exist
		_, err = svc.GetByID(p.ID)
		if err == nil {
			t.Log("Product should not exist after deletion")
			return false
		}

		// Document's product_id should be empty string
		var docProductID string
		err = db.QueryRow("SELECT product_id FROM documents WHERE id = ?", docID).Scan(&docProductID)
		if err != nil {
			t.Logf("Query document product_id failed: %v", err)
			return false
		}
		if docProductID != "" {
			t.Logf("Document product_id should be empty after product deletion, got %q", docProductID)
			return false
		}

		// Chunk's product_id should be empty string
		var chunkProductID string
		err = db.QueryRow("SELECT product_id FROM chunks WHERE id = ?", chunkID).Scan(&chunkProductID)
		if err != nil {
			t.Logf("Query chunk product_id failed: %v", err)
			return false
		}
		if chunkProductID != "" {
			t.Logf("Chunk product_id should be empty after product deletion, got %q", chunkProductID)
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestProperty4_AdminProductAssignmentRoundTrip verifies that assigning products
// to an admin user and querying back returns the exact same set, and reassigning
// returns the new set.
//
// **Feature: multi-product-support, Property 4: 管理员-产品分配往返一致性**
// **Validates: Requirements 2.1, 2.2, 2.3, 9.4**
func TestProperty4_AdminProductAssignmentRoundTrip(t *testing.T) {
	counter := 0
	f := func(numProducts uint8) bool {
		db, cleanup := setupTestDB(t)
		defer cleanup()
		svc := NewProductService(db)

		counter++

		// Create an admin user (needed for FK constraint)
		adminID := fmt.Sprintf("admin_%d", counter)
		_, err := db.Exec(
			"INSERT INTO admin_users (id, username, password_hash, role) VALUES (?, ?, 'hash', 'editor')",
			adminID, fmt.Sprintf("user_%d", counter),
		)
		if err != nil {
			t.Logf("Insert admin user failed: %v", err)
			return false
		}

		// Create 1-5 products
		n := int(numProducts)%5 + 1
		var productIDs []string
		for i := 0; i < n; i++ {
			p, err := svc.Create(fmt.Sprintf("prod_%d_%d", counter, i), "service", "desc", "")
			if err != nil {
				t.Logf("Create product failed: %v", err)
				return false
			}
			productIDs = append(productIDs, p.ID)
		}

		// Assign first subset (first half)
		half := len(productIDs) / 2
		if half == 0 {
			half = 1
		}
		firstSet := productIDs[:half]
		err = svc.AssignAdminUser(adminID, firstSet)
		if err != nil {
			t.Logf("AssignAdminUser (first) failed: %v", err)
			return false
		}

		// Query and verify first assignment
		got, err := svc.GetByAdminUserID(adminID)
		if err != nil {
			t.Logf("GetByAdminUserID (first) failed: %v", err)
			return false
		}
		if !sameIDSet(got, firstSet) {
			t.Logf("First assignment mismatch: got %v, want %v", extractIDs(got), firstSet)
			return false
		}

		// Reassign to all products (full set) to test replacement
		err = svc.AssignAdminUser(adminID, productIDs)
		if err != nil {
			t.Logf("AssignAdminUser (full) failed: %v", err)
			return false
		}

		// Query and verify full assignment
		got2, err := svc.GetByAdminUserID(adminID)
		if err != nil {
			t.Logf("GetByAdminUserID (full) failed: %v", err)
			return false
		}
		if !sameIDSet(got2, productIDs) {
			t.Logf("Full assignment mismatch: got %v, want %v", extractIDs(got2), productIDs)
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// sameIDSet checks if the product list contains exactly the expected IDs (order-independent).
func sameIDSet(products []Product, expectedIDs []string) bool {
	if len(products) != len(expectedIDs) {
		return false
	}
	got := make(map[string]bool)
	for _, p := range products {
		got[p.ID] = true
	}
	for _, id := range expectedIDs {
		if !got[id] {
			return false
		}
	}
	return true
}

// extractIDs returns the IDs from a product slice for logging.
func extractIDs(products []Product) []string {
	ids := make([]string, len(products))
	for i, p := range products {
		ids[i] = p.ID
	}
	return ids
}

// TestProperty5_AdminProductSelectionLogic verifies that:
// - An admin assigned exactly one product gets exactly one product back
// - An admin assigned multiple products gets multiple products back
// - An admin assigned zero products gets ALL products back
//
// **Feature: multi-product-support, Property 5: 管理员产品选择逻辑**
// **Validates: Requirements 3.1, 3.2, 2.4**
func TestProperty5_AdminProductSelectionLogic(t *testing.T) {
	counter := 0
	f := func(numProducts uint8) bool {
		db, cleanup := setupTestDB(t)
		defer cleanup()
		svc := NewProductService(db)

		counter++

		// Create admin user
		adminID := fmt.Sprintf("admin_%d", counter)
		_, err := db.Exec(
			"INSERT INTO admin_users (id, username, password_hash, role) VALUES (?, ?, 'hash', 'editor')",
			adminID, fmt.Sprintf("user_%d", counter),
		)
		if err != nil {
			t.Logf("Insert admin user failed: %v", err)
			return false
		}

		// Create 2-6 products (need at least 2 for the "multiple" case)
		n := int(numProducts)%5 + 2
		var allProductIDs []string
		for i := 0; i < n; i++ {
			p, err := svc.Create(fmt.Sprintf("prod_%d_%d", counter, i), "service", "desc", "")
			if err != nil {
				t.Logf("Create product failed: %v", err)
				return false
			}
			allProductIDs = append(allProductIDs, p.ID)
		}

		// Case 1: Assign exactly one product
		err = svc.AssignAdminUser(adminID, allProductIDs[:1])
		if err != nil {
			t.Logf("AssignAdminUser (one) failed: %v", err)
			return false
		}
		got, err := svc.GetByAdminUserID(adminID)
		if err != nil {
			t.Logf("GetByAdminUserID (one) failed: %v", err)
			return false
		}
		if len(got) != 1 {
			t.Logf("Expected exactly 1 product for single assignment, got %d", len(got))
			return false
		}

		// Case 2: Assign multiple products (at least 2)
		multiSet := allProductIDs[:2]
		err = svc.AssignAdminUser(adminID, multiSet)
		if err != nil {
			t.Logf("AssignAdminUser (multi) failed: %v", err)
			return false
		}
		got, err = svc.GetByAdminUserID(adminID)
		if err != nil {
			t.Logf("GetByAdminUserID (multi) failed: %v", err)
			return false
		}
		if len(got) < 2 {
			t.Logf("Expected multiple products for multi assignment, got %d", len(got))
			return false
		}

		// Case 3: Assign zero products → should return ALL products
		err = svc.AssignAdminUser(adminID, []string{})
		if err != nil {
			t.Logf("AssignAdminUser (zero) failed: %v", err)
			return false
		}
		got, err = svc.GetByAdminUserID(adminID)
		if err != nil {
			t.Logf("GetByAdminUserID (zero) failed: %v", err)
			return false
		}
		if len(got) != n {
			t.Logf("Expected all %d products for zero assignment, got %d", n, len(got))
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}
