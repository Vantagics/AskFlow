// Package db provides SQLite database initialization and migration for the askflow system.
package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// InitDB opens a SQLite database connection at dbPath, enables WAL mode and
// foreign keys, and creates all required tables idempotently.
func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool for SQLite
	// WAL mode allows concurrent readers with one writer.
	// Use multiple connections so reads don't block behind writes.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0) // connections don't expire

	if err := configurePragmas(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := createTables(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := createAdminUsersTable(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create admin_users table: %w", err)
	}

	if err := createProductTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create product tables: %w", err)
	}

	if err := migrateTables(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := migrateProductTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate product tables: %w", err)
	}

	if err := createLoginAttemptsTable(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create login_attempts table: %w", err)
	}

	if err := createIndexes(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func configurePragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=30000",
		"PRAGMA secure_delete=ON",
		"PRAGMA wal_autocheckpoint=1000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("failed to execute %s: %w", p, err)
		}
	}
	return nil
}

func createTables(db *sql.DB) error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			type       TEXT NOT NULL,
			status     TEXT NOT NULL,
			error      TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id            TEXT PRIMARY KEY,
			document_id   TEXT NOT NULL,
			document_name TEXT NOT NULL,
			chunk_index   INTEGER NOT NULL,
			chunk_text    TEXT NOT NULL,
			embedding     BLOB NOT NULL,
			image_url     TEXT DEFAULT '',
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (document_id) REFERENCES documents(id)
		)`,
		`CREATE TABLE IF NOT EXISTS pending_questions (
			id          TEXT PRIMARY KEY,
			question    TEXT NOT NULL,
			user_id     TEXT NOT NULL,
			status      TEXT NOT NULL,
			answer      TEXT,
			llm_answer  TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			answered_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id             TEXT PRIMARY KEY,
			email          TEXT UNIQUE,
			name           TEXT,
			provider       TEXT NOT NULL,
			provider_id    TEXT NOT NULL,
			password_hash  TEXT,
			email_verified INTEGER DEFAULT 0,
			created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login     DATETIME,
			default_product_id TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS email_tokens (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			token      TEXT NOT NULL UNIQUE,
			type       TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS video_segments (
			id           TEXT PRIMARY KEY,
			document_id  TEXT NOT NULL,
			segment_type TEXT NOT NULL,
			start_time   REAL NOT NULL,
			end_time     REAL NOT NULL,
			content      TEXT NOT NULL,
			chunk_id     TEXT NOT NULL,
			FOREIGN KEY (document_id) REFERENCES documents(id)
		)`,
		`CREATE TABLE IF NOT EXISTS sn_users (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			email          TEXT UNIQUE NOT NULL,
			display_name   TEXT NOT NULL,
			sn             TEXT DEFAULT '',
			last_login_at  DATETIME,
			created_at     DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS login_tickets (
			ticket      TEXT PRIMARY KEY,
			user_id     INTEGER NOT NULL,
			used        INTEGER DEFAULT 0,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at  DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES sn_users(id)
		)`,
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	for _, ddl := range tables {
		if _, err := tx.Exec(ddl); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	return tx.Commit()
}

// createAdminUsersTable creates the admin_users table for sub-account management.
// Called separately after main tables since it may not exist in older DBs.
func createAdminUsersTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS admin_users (
		id            TEXT PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role          TEXT NOT NULL DEFAULT 'editor',
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	return err
}

// createProductTables creates the products table and admin_user_products junction table.
// Called after createAdminUsersTable since admin_user_products references admin_users.
func createProductTables(db *sql.DB) error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS products (
			id              TEXT PRIMARY KEY,
			name            TEXT NOT NULL UNIQUE,
			description     TEXT DEFAULT '',
			welcome_message TEXT DEFAULT '',
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS admin_user_products (
			admin_user_id TEXT NOT NULL,
			product_id    TEXT NOT NULL,
			PRIMARY KEY (admin_user_id, product_id),
			FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE,
			FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE CASCADE
		)`,
	}

	for _, ddl := range tables {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("failed to create product table: %w", err)
		}
	}
	return nil
}

// migrateProductTables adds missing columns to product tables for backward compatibility.
// Called after createProductTables to ensure the table exists before altering it.
func migrateProductTables(db *sql.DB) error {
	migrations := []struct {
		table  string
		column string
		ddl    string
	}{
		{"products", "welcome_message", "ALTER TABLE products ADD COLUMN welcome_message TEXT DEFAULT ''"},
		{"products", "type", "ALTER TABLE products ADD COLUMN type TEXT DEFAULT 'service'"},
		{"products", "allow_download", "ALTER TABLE products ADD COLUMN allow_download INTEGER DEFAULT 0"},
	}

	for _, m := range migrations {
		if !columnExists(db, m.table, m.column) {
			if _, err := db.Exec(m.ddl); err != nil {
				return fmt.Errorf("migration failed (%s.%s): %w", m.table, m.column, err)
			}
		}
	}
	return nil
}

// createLoginAttemptsTable creates the table for tracking admin login attempts.
func createLoginAttemptsTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS login_attempts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT NOT NULL,
		ip         TEXT NOT NULL,
		success    INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS login_bans (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT NOT NULL DEFAULT '',
		ip         TEXT NOT NULL DEFAULT '',
		reason     TEXT NOT NULL DEFAULT '',
		unlocks_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	return err
}

// createIndexes adds indexes for frequently queried columns.
// Called after migrations to ensure all columns exist.
func createIndexes(db *sql.DB) error {
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_chunks_document_id ON chunks(document_id)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_content_hash ON documents(content_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_status ON documents(status)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_email_tokens_token ON email_tokens(token)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_product_id ON documents(product_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_product_id ON chunks(product_id)`,
		`CREATE INDEX IF NOT EXISTS idx_video_segments_chunk_id ON video_segments(chunk_id)`,
		`CREATE INDEX IF NOT EXISTS idx_video_segments_document_id ON video_segments(document_id)`,
		`CREATE INDEX IF NOT EXISTS idx_login_attempts_username ON login_attempts(username, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_login_attempts_ip ON login_attempts(ip, created_at)`,
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}
	return nil
}

// migrateTables adds missing columns to existing tables for backward compatibility.
func migrateTables(db *sql.DB) error {
	// Each migration: table, column, DDL to add it
	migrations := []struct {
		table  string
		column string
		ddl    string
	}{
		{"users", "password_hash", "ALTER TABLE users ADD COLUMN password_hash TEXT"},
		{"users", "email_verified", "ALTER TABLE users ADD COLUMN email_verified INTEGER DEFAULT 0"},
		{"users", "last_login", "ALTER TABLE users ADD COLUMN last_login DATETIME"},
		{"users", "created_at", "ALTER TABLE users ADD COLUMN created_at DATETIME DEFAULT CURRENT_TIMESTAMP"},
		{"users", "default_product_id", "ALTER TABLE users ADD COLUMN default_product_id TEXT DEFAULT ''"},
		{"chunks", "image_url", "ALTER TABLE chunks ADD COLUMN image_url TEXT DEFAULT ''"},
		{"documents", "content_hash", "ALTER TABLE documents ADD COLUMN content_hash TEXT DEFAULT ''"},
		{"pending_questions", "image_data", "ALTER TABLE pending_questions ADD COLUMN image_data TEXT DEFAULT ''"},
		{"documents", "product_id", "ALTER TABLE documents ADD COLUMN product_id TEXT DEFAULT ''"},
		{"chunks", "product_id", "ALTER TABLE chunks ADD COLUMN product_id TEXT DEFAULT ''"},
		{"pending_questions", "product_id", "ALTER TABLE pending_questions ADD COLUMN product_id TEXT DEFAULT ''"},
		{"admin_users", "permissions", "ALTER TABLE admin_users ADD COLUMN permissions TEXT DEFAULT ''"},
	}

	for _, m := range migrations {
		if !columnExists(db, m.table, m.column) {
			if _, err := db.Exec(m.ddl); err != nil {
				return fmt.Errorf("migration failed (%s.%s): %w", m.table, m.column, err)
			}
		}
	}
	return nil
}

// columnExists checks if a column exists in a table.
// Table names are validated against a whitelist to prevent SQL injection.
func columnExists(db *sql.DB, table, column string) bool {
	// Whitelist of known tables to prevent SQL injection via table name
	validTables := map[string]bool{
		"users": true, "documents": true, "chunks": true,
		"pending_questions": true, "sessions": true,
		"email_tokens": true, "admin_users": true,
		"products": true, "admin_user_products": true,
		"video_segments": true,
	}
	if !validTables[table] {
		return false
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}
