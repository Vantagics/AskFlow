// Package db provides SQLite database initialization and migration for the helpdesk system.
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

	if err := configurePragmas(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := createTables(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := migrateTables(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := createAdminUsersTable(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create admin_users table: %w", err)
	}

	return db, nil
}

func configurePragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
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
			last_login     DATETIME
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
		{"chunks", "image_url", "ALTER TABLE chunks ADD COLUMN image_url TEXT DEFAULT ''"},
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
func columnExists(db *sql.DB, table, column string) bool {
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
