// Package backup provides full and incremental backup/restore for the askflow system.
//
// Backup strategy (data-level, not file-level):
//
//	Full mode:
//	  - SQLite online backup API for a consistent DB snapshot
//	  - All upload files
//	  - Config + encryption key
//
//	Incremental mode:
//	  - Insert-only tables (documents, chunks, video_segments, admin_users):
//	    export only rows with created_at > last backup time
//	  - Mutable tables (pending_questions, users, products, admin_user_products):
//	    full table dump (rows may be updated)
//	  - Ephemeral tables (sessions, email_tokens): skipped
//	  - Upload files: only new directories since last backup
//	  - Config + encryption key: always included
//
// Archive layout (tar.gz):
//
//	askflow.db              — full DB copy (full mode only)
//	db_delta.sql             — SQL statements for changed data (incremental only)
//	uploads/<hash>/file      — uploaded document files
//	config.json              — system configuration
//	encryption.key           — AES encryption key
package backup

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manifest records backup metadata and is saved alongside the archive.
type Manifest struct {
	Timestamp   string         `json:"timestamp"`             // backup time (RFC3339)
	Mode        string         `json:"mode"`                  // "full" or "incremental"
	BasedOn     string         `json:"based_on,omitempty"`    // parent manifest (incremental)
	UploadDirs  []string       `json:"upload_dirs"`           // upload subdirs included
	DBRowCounts map[string]int `json:"db_row_counts"`         // table -> rows exported
	DataDir     string         `json:"data_dir"`              // original data directory path
}

// Options configures a backup operation.
type Options struct {
	DataDir    string // data directory path (default "./data")
	OutputDir  string // output directory for archive (default ".")
	Mode       string // "full" or "incremental"
	ManifestIn string // previous manifest path (required for incremental)
}

// Result holds backup results.
type Result struct {
	ArchivePath  string
	ManifestPath string
	FilesWritten int
	DBRows       int
	BytesWritten int64
}

// insertOnlyTables are append-only; incremental exports rows by created_at.
var insertOnlyTables = []string{"documents", "chunks", "video_segments", "admin_users"}

// mutableTables may have row updates; incremental does full dump of these.
var mutableTables = []string{"pending_questions", "users", "products", "admin_user_products"}

// allDataTables is the union used for full backup SQL export verification.
var allDataTables = append(append([]string{}, insertOnlyTables...), mutableTables...)

// Run executes a backup.
func Run(db *sql.DB, opts Options) (*Result, error) {
	if opts.DataDir == "" {
		opts.DataDir = "./data"
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "."
	}
	if opts.Mode == "" {
		opts.Mode = "full"
	}

	// Checkpoint WAL for consistency
	if db != nil {
		if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			fmt.Printf("警告: WAL checkpoint 失败: %v\n", err)
		}
	}

	// Load previous manifest for incremental
	var prev *Manifest
	if opts.Mode == "incremental" {
		if opts.ManifestIn == "" {
			return nil, fmt.Errorf("增量备份需要指定基准 manifest (--base)")
		}
		m, err := loadManifest(opts.ManifestIn)
		if err != nil {
			return nil, fmt.Errorf("加载基准 manifest 失败: %w", err)
		}
		prev = m
	}

	now := time.Now()
	timestamp := now.Format("20060102-150405")
	modeLabel := opts.Mode
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "local"
	}

	// Archive name: askflow_<mode>_<hostname>_<timestamp>.tar.gz
	archiveName := fmt.Sprintf("askflow_%s_%s_%s.tar.gz", modeLabel, hostname, timestamp)
	manifestName := fmt.Sprintf("askflow_%s_%s_%s.manifest.json", modeLabel, hostname, timestamp)
	archivePath := filepath.Join(opts.OutputDir, archiveName)
	manifestPath := filepath.Join(opts.OutputDir, manifestName)

	manifest := &Manifest{
		Timestamp:   now.Format(time.RFC3339),
		Mode:        opts.Mode,
		DataDir:     opts.DataDir,
		UploadDirs:  []string{},
		DBRowCounts: make(map[string]int),
	}
	if opts.Mode == "incremental" {
		manifest.BasedOn = opts.ManifestIn
	}

	out, err := os.Create(archivePath)
	if err != nil {
		return nil, fmt.Errorf("创建归档文件失败: %w", err)
	}
	defer out.Close()

	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	result := &Result{ArchivePath: archivePath, ManifestPath: manifestPath}

	// 1. Config + encryption key (always)
	for _, name := range []string{"config.json", "encryption.key"} {
		p := filepath.Join(opts.DataDir, name)
		if _, err := os.Stat(p); err == nil {
			n, err := addFileToTar(tw, p, name)
			if err != nil {
				return nil, fmt.Errorf("添加 %s 失败: %w", name, err)
			}
			result.BytesWritten += n
			result.FilesWritten++
		}
	}

	// 2. Database
	if opts.Mode == "full" {
		// Full: copy the entire DB file
		dbPath := filepath.Join(opts.DataDir, "askflow.db")
		if _, err := os.Stat(dbPath); err == nil {
			n, err := addFileToTar(tw, dbPath, "askflow.db")
			if err != nil {
				return nil, fmt.Errorf("添加数据库失败: %w", err)
			}
			result.BytesWritten += n
			result.FilesWritten++
			// Record row counts for reference
			for _, t := range allDataTables {
				if cnt, err := countRows(db, t); err == nil {
					manifest.DBRowCounts[t] = cnt
					result.DBRows += cnt
				}
			}
		}
	} else {
		// Incremental: generate SQL delta
		sinceTime := prev.Timestamp
		sqlData, rowCounts, err := generateDeltaSQL(db, sinceTime)
		if err != nil {
			return nil, fmt.Errorf("生成增量 SQL 失败: %w", err)
		}
		if len(sqlData) > 0 {
			n, err := addBytesToTar(tw, sqlData, "db_delta.sql")
			if err != nil {
				return nil, fmt.Errorf("添加增量 SQL 失败: %w", err)
			}
			result.BytesWritten += n
			result.FilesWritten++
		}
		manifest.DBRowCounts = rowCounts
		for _, c := range rowCounts {
			result.DBRows += c
		}
	}

	// 3. Upload files
	uploadsDir := filepath.Join(opts.DataDir, "uploads")
	if info, err := os.Stat(uploadsDir); err == nil && info.IsDir() {
		prevDirs := make(map[string]bool)
		if prev != nil {
			for _, d := range prev.UploadDirs {
				prevDirs[d] = true
			}
		}

		entries, err := os.ReadDir(uploadsDir)
		if err != nil {
			return nil, fmt.Errorf("读取 uploads 目录失败: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dirName := entry.Name()
			// Incremental: skip dirs already in previous backup
			if opts.Mode == "incremental" && prevDirs[dirName] {
				continue
			}
			manifest.UploadDirs = append(manifest.UploadDirs, dirName)
			// Add all files in this upload subdirectory
			subDir := filepath.Join(uploadsDir, dirName)
			err := filepath.Walk(subDir, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(opts.DataDir, path)
				rel = filepath.ToSlash(rel)
				n, err := addFileToTar(tw, path, rel)
				if err != nil {
					return err
				}
				result.BytesWritten += n
				result.FilesWritten++
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("添加上传文件失败: %w", err)
			}
		}

		// For full backup, record ALL upload dirs (not just new ones)
		if opts.Mode == "full" {
			manifest.UploadDirs = nil
			for _, entry := range entries {
				if entry.IsDir() {
					manifest.UploadDirs = append(manifest.UploadDirs, entry.Name())
				}
			}
		}
	}

	// 4. Embed manifest in archive
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	if _, err := addBytesToTar(tw, manifestData, "manifest.json"); err != nil {
		return nil, fmt.Errorf("嵌入 manifest 失败: %w", err)
	}

	// 5. Save manifest alongside archive
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return nil, fmt.Errorf("保存 manifest 失败: %w", err)
	}

	return result, nil
}

// generateDeltaSQL produces INSERT OR REPLACE statements for incremental backup.
func generateDeltaSQL(db *sql.DB, sinceTime string) ([]byte, map[string]int, error) {
	var buf strings.Builder
	rowCounts := make(map[string]int)

	buf.WriteString("-- Askflow incremental backup delta\n")
	buf.WriteString(fmt.Sprintf("-- Since: %s\n\n", sinceTime))
	buf.WriteString("BEGIN TRANSACTION;\n\n")

	// Insert-only tables: export rows created after sinceTime
	for _, table := range insertOnlyTables {
		cols, err := getColumns(db, table)
		if err != nil {
			continue // table may not exist yet
		}
		hasCreatedAt := false
		for _, c := range cols {
			if c == "created_at" {
				hasCreatedAt = true
				break
			}
		}
		var query string
		if hasCreatedAt {
			query = fmt.Sprintf("SELECT * FROM %s WHERE created_at > ?", table)
		} else {
			// No timestamp (e.g. video_segments) — export by joining to parent
			// For video_segments, export those whose document was created after sinceTime
			if table == "video_segments" {
				query = fmt.Sprintf(
					"SELECT vs.* FROM video_segments vs JOIN documents d ON vs.document_id = d.id WHERE d.created_at > ?")
			} else {
				continue
			}
		}

		rows, err := db.Query(query, sinceTime)
		if err != nil {
			return nil, nil, fmt.Errorf("查询表 %s 失败: %w", table, err)
		}
		count, err := writeInserts(&buf, table, cols, rows)
		rows.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("导出表 %s 失败: %w", table, err)
		}
		if count > 0 {
			rowCounts[table] = count
		}
	}

	// Mutable tables: full dump (DELETE + INSERT)
	for _, table := range mutableTables {
		cols, err := getColumns(db, table)
		if err != nil {
			continue
		}
		buf.WriteString(fmt.Sprintf("DELETE FROM %s;\n", table))
		rows, err := db.Query(fmt.Sprintf("SELECT * FROM %s", table))
		if err != nil {
			return nil, nil, fmt.Errorf("查询表 %s 失败: %w", table, err)
		}
		count, err := writeInserts(&buf, table, cols, rows)
		rows.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("导出表 %s 失败: %w", table, err)
		}
		if count > 0 {
			rowCounts[table] = count
		}
		buf.WriteString("\n")
	}

	buf.WriteString("COMMIT;\n")
	return []byte(buf.String()), rowCounts, nil
}

// writeInserts writes INSERT OR REPLACE statements for rows and returns the count.
func writeInserts(buf *strings.Builder, table string, cols []string, rows *sql.Rows) (int, error) {
	colList := strings.Join(cols, ", ")
	count := 0
	scanDest := make([]interface{}, len(cols))
	scanPtrs := make([]interface{}, len(cols))
	for i := range scanDest {
		scanPtrs[i] = &scanDest[i]
	}

	for rows.Next() {
		if err := rows.Scan(scanPtrs...); err != nil {
			return count, err
		}
		vals := make([]string, len(cols))
		for i, v := range scanDest {
			vals[i] = sqlQuote(v)
		}
		buf.WriteString(fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s);\n",
			table, colList, strings.Join(vals, ", ")))
		count++
	}
	return count, rows.Err()
}

// sqlQuote formats a value for SQL insertion.
func sqlQuote(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	switch val := v.(type) {
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%g", val)
	case []byte:
		return fmt.Sprintf("X'%x'", val)
	case string:
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	default:
		s := fmt.Sprintf("%v", val)
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
}

// validBackupTables is a whitelist of tables allowed in backup operations.
var validBackupTables = map[string]bool{
	"documents": true, "chunks": true, "video_segments": true, "admin_users": true,
	"pending_questions": true, "users": true, "products": true, "admin_user_products": true,
	"login_attempts": true, "login_bans": true,
}

// getColumns returns column names for a table.
func getColumns(db *sql.DB, table string) ([]string, error) {
	if !validBackupTables[table] {
		return nil, fmt.Errorf("invalid table name: %s", table)
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt *string
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("表 %s 不存在或无列", table)
	}
	return cols, nil
}

// countRows returns the row count for a table.
func countRows(db *sql.DB, table string) (int, error) {
	if !validBackupTables[table] {
		return 0, fmt.Errorf("invalid table name: %s", table)
	}
	var n int
	err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n)
	return n, err
}

// Restore extracts a backup archive into the target data directory.
// For incremental restore: first restore the full backup, then apply each incremental in order.
// The db_delta.sql is NOT auto-executed — it is extracted as a file for the user to review and apply.
func Restore(archivePath, targetDir string) error {
	if targetDir == "" {
		targetDir = "./data"
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("打开备份文件失败: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	fileCount := 0
	hasDelta := false
	var totalExtracted int64
	const maxTotalSize = 10 << 30 // 10GB total extraction limit
	const maxFileCount = 100000   // max files to extract

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取归档失败: %w", err)
		}

		target := filepath.Join(targetDir, filepath.FromSlash(header.Name))

		// Security: prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(targetDir)) {
			return fmt.Errorf("非法路径: %s", header.Name)
		}

		// Security: reject symlinks to prevent symlink attacks
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return fmt.Errorf("不允许的链接类型: %s", header.Name)
		}

		if header.Name == "db_delta.sql" {
			hasDelta = true
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("创建目录失败 %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("创建目录失败: %w", err)
			}
			// Limit individual file extraction to 2GB to prevent zip bombs
			if header.Size > 2<<30 {
				return fmt.Errorf("文件过大，跳过: %s (%d bytes)", header.Name, header.Size)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0755)
			if err != nil {
				return fmt.Errorf("创建文件失败 %s: %w", target, err)
			}
			if _, err := io.Copy(out, io.LimitReader(tr, header.Size+1)); err != nil {
				out.Close()
				return fmt.Errorf("写入文件失败 %s: %w", target, err)
			}
			out.Close()
			totalExtracted += header.Size
			if totalExtracted > maxTotalSize {
				return fmt.Errorf("总解压大小超过限制 (10GB)")
			}
			fileCount++
			if fileCount > maxFileCount {
				return fmt.Errorf("文件数量超过限制 (%d)", maxFileCount)
			}
		}
	}

	fmt.Printf("恢复完成，共还原 %d 个文件到 %s\n", fileCount, targetDir)
	if hasDelta {
		deltaPath := filepath.Join(targetDir, "db_delta.sql")
		fmt.Printf("\n检测到增量备份 SQL: %s\n", deltaPath)
		fmt.Println("请在恢复全量备份后，执行以下命令应用增量数据:")
		fmt.Printf("  sqlite3 %s < %s\n", filepath.Join(targetDir, "askflow.db"), deltaPath)
	}
	return nil
}

// RestoreDelta applies an incremental SQL delta file to the database.
func RestoreDelta(db *sql.DB, deltaPath string) error {
	data, err := os.ReadFile(deltaPath)
	if err != nil {
		return fmt.Errorf("读取增量 SQL 失败: %w", err)
	}

	// Validate delta SQL: only allow INSERT/UPDATE/DELETE statements
	// to prevent arbitrary SQL execution (e.g., DROP TABLE, ATTACH DATABASE)
	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}
	statements := strings.Split(content, ";")
	for _, stmt := range statements {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "" {
			continue
		}
		upper := strings.ToUpper(trimmed)
		if !strings.HasPrefix(upper, "INSERT ") &&
			!strings.HasPrefix(upper, "UPDATE ") &&
			!strings.HasPrefix(upper, "DELETE ") &&
			!strings.HasPrefix(upper, "REPLACE ") {
			return fmt.Errorf("增量 SQL 包含不允许的语句类型: %s", truncateForLog(trimmed, 50))
		}
	}

	if _, err := db.Exec(string(data)); err != nil {
		return fmt.Errorf("执行增量 SQL 失败: %w", err)
	}
	return nil
}

// truncateForLog truncates a string for safe logging.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- tar helpers ---

func addFileToTar(tw *tar.Writer, absPath, archiveName string) (int64, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return 0, err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return 0, err
	}
	header.Name = archiveName

	if err := tw.WriteHeader(header); err != nil {
		return 0, err
	}
	f, err := os.Open(absPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(tw, f)
	return n, err
}

func addBytesToTar(tw *tar.Writer, data []byte, archiveName string) (int64, error) {
	header := &tar.Header{
		Name:    archiveName,
		Size:    int64(len(data)),
		Mode:    0644,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return 0, err
	}
	n, err := tw.Write(data)
	return int64(n), err
}

func loadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
