// Package errlog provides a dedicated error-only file logger that writes
// to /var/log/askflow/error.log (Linux) or logs/error.log (Windows).
//
// Features:
//   - Only ERROR level messages are recorded
//   - Automatic log rotation when file exceeds maxFileSize (10MB default)
//   - Rotated logs are gzip-compressed to save disk space
//   - Retains up to maxBackups compressed archives (5 default)
//   - Thread-safe: all operations are protected by a mutex
package errlog

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultLogDir = "/var/log/askflow"
	windowsLogDir = "logs"
	logFileName   = "error.log"

	// maxFileSize is the threshold in bytes before rotation (100 MB).
	maxFileSize = 100 << 20
	// maxBackups is the number of compressed archives to keep.
	maxBackups = 5
	// writeBufSize is the size of the internal write buffer.
	writeBufSize = 4096
)

// logger is the package-level singleton.
var (
	global *errorLogger
	mu     sync.Mutex // protects Init / Close and the global pointer
)

// errorLogger holds the state for the rotating error log writer.
type errorLogger struct {
	mu          sync.Mutex
	file        *os.File
	dir         string
	path        string
	size        int64
	buf         []byte // reusable format buffer to reduce allocations
	closed      bool
	maxRotSize  int64  // configurable rotation threshold in bytes
}

// Init initializes the error logger. It is safe to call multiple times;
// if the logger is already running the call is a no-op. If a previous Init
// failed, calling Init again will retry.
func Init() error {
	mu.Lock()
	defer mu.Unlock()

	if global != nil {
		return nil // already initialised
	}

	dir := defaultLogDir
	if runtime.GOOS == "windows" {
		dir = windowsLogDir
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create error log directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open error log file %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat error log file: %w", err)
	}

	global = &errorLogger{
		file:       f,
		dir:        dir,
		path:       path,
		size:       info.Size(),
		buf:        make([]byte, 0, writeBufSize),
		maxRotSize: maxFileSize,
	}
	return nil
}

// Logf writes a formatted error message to the error log file.
// If the logger is not initialized the call is silently ignored.
func Logf(format string, args ...interface{}) {
	mu.Lock()
	l := global
	mu.Unlock()

	if l == nil {
		return
	}
	l.logf(format, args...)
}

// Close flushes and closes the error log file. Call on application shutdown.
func Close() {
	mu.Lock()
	defer mu.Unlock()

	if global == nil {
		return
	}
	global.close()
	global = nil
}

// --- internal methods on errorLogger ---

// logf formats the message, writes it, and triggers rotation if needed.
func (l *errorLogger) logf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || l.file == nil {
		return
	}

	// Format: "2006/01/02 15:04:05 [ERROR] <message>\n"
	now := time.Now()
	l.buf = l.buf[:0]
	l.buf = now.AppendFormat(l.buf, "2006/01/02 15:04:05")
	l.buf = append(l.buf, " [ERROR] "...)
	l.buf = fmt.Appendf(l.buf, format, args...)
	if len(l.buf) == 0 || l.buf[len(l.buf)-1] != '\n' {
		l.buf = append(l.buf, '\n')
	}

	n, err := l.file.Write(l.buf)
	if err != nil {
		// Write failed — not much we can do; avoid cascading errors.
		return
	}
	l.size += int64(n)

	// Check if rotation is needed after write.
	if l.size >= l.maxRotSize {
		l.rotate()
	}
}

// rotate compresses the current log file and opens a fresh one.
// Caller must hold l.mu.
func (l *errorLogger) rotate() {
	// Sync and close current file before renaming.
	l.file.Sync()
	l.file.Close()
	l.file = nil

	// Build archive name: error-20260219-153045.log.gz
	ts := time.Now().Format("20060102-150405")
	archiveName := fmt.Sprintf("error-%s.log.gz", ts)
	archivePath := filepath.Join(l.dir, archiveName)

	// Compress the current log into the archive.
	if err := compressFile(l.path, archivePath); err != nil {
		// Compression failed — try to truncate the original to avoid
		// unbounded growth, then reopen.
		os.Truncate(l.path, 0)
	} else {
		// Compression succeeded — remove the original content.
		os.Truncate(l.path, 0)
	}

	// Prune old archives beyond maxBackups.
	l.pruneArchives()

	// Reopen the (now empty) log file.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Cannot reopen — logger is effectively dead until next Init.
		return
	}
	l.file = f
	l.size = 0
}

// pruneArchives removes the oldest compressed archives if there are more
// than maxBackups. Caller must hold l.mu.
func (l *errorLogger) pruneArchives() {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}

	var archives []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "error-") && strings.HasSuffix(name, ".log.gz") {
			archives = append(archives, name)
		}
	}

	if len(archives) <= maxBackups {
		return
	}

	// Sort ascending by name (timestamp in name ensures chronological order).
	sort.Strings(archives)

	// Remove the oldest ones.
	toRemove := archives[:len(archives)-maxBackups]
	for _, name := range toRemove {
		os.Remove(filepath.Join(l.dir, name))
	}
}

// close syncs and closes the underlying file. Caller must hold the package mu.
func (l *errorLogger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = true
	if l.file != nil {
		l.file.Sync()
		l.file.Close()
		l.file = nil
	}
}

// compressFile reads src, writes gzip-compressed data to dst, and returns
// any error. On failure the partial dst file is removed.
func compressFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	gw, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}

	if _, err := io.Copy(gw, in); err != nil {
		gw.Close()
		out.Close()
		os.Remove(dst)
		return err
	}

	// Must close gzip writer before the file to flush the footer.
	if err := gw.Close(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// --- Exported helpers for log management API ---

// GetLogDir returns the log directory path.
func GetLogDir() string {
	if runtime.GOOS == "windows" {
		return windowsLogDir
	}
	return defaultLogDir
}

// GetLogPath returns the full path to the current error log file.
func GetLogPath() string {
	return filepath.Join(GetLogDir(), logFileName)
}

// GetRotationSizeMB returns the current rotation threshold in megabytes.
func GetRotationSizeMB() int {
	mu.Lock()
	defer mu.Unlock()
	if global != nil {
		return int(global.maxRotSize >> 20)
	}
	return int(maxFileSize >> 20)
}

// SetRotationSizeMB updates the rotation threshold. sizeMB must be >= 1.
func SetRotationSizeMB(sizeMB int) {
	if sizeMB < 1 {
		sizeMB = 1
	}
	mu.Lock()
	defer mu.Unlock()
	if global != nil {
		global.mu.Lock()
		global.maxRotSize = int64(sizeMB) << 20
		global.mu.Unlock()
	}
}

// RecentLines reads the last n lines from the current error log file.
// It returns at most n lines in chronological order (oldest first).
func RecentLines(n int) ([]string, error) {
	if n <= 0 {
		n = 50
	}
	path := GetLogPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return []string{}, nil
	}

	// Cap the read to avoid scanning huge files — 50 lines × ~200 bytes ≈ 10KB typical,
	// but allow up to 256KB to handle long lines gracefully.
	const maxRead = 256 * 1024
	readStart := int64(0)
	if size > maxRead {
		readStart = size - maxRead
	}
	readLen := size - readStart

	buf := make([]byte, readLen)
	_, err = f.ReadAt(buf, readStart)
	if err != nil && err != io.EOF {
		return nil, err
	}

	// Split from the end: walk backwards counting newlines.
	// We need n lines, which means n newline-terminated segments from the end.
	lines := make([]string, 0, n)
	end := len(buf)
	// Skip trailing newline if present
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0 && len(lines) < n; i-- {
		if buf[i] == '\n' {
			line := string(buf[i+1 : end])
			if line != "" {
				lines = append(lines, line)
			}
			end = i
		}
	}
	// Handle the first line (no leading newline)
	if len(lines) < n && end > 0 {
		line := string(buf[:end])
		if line != "" {
			lines = append(lines, line)
		}
	}

	// Reverse to chronological order (oldest first)
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines, nil
}

// ListArchives returns the names of compressed log archives in the log directory.
func ListArchives() ([]string, error) {
	dir := GetLogDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var archives []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "error-") && strings.HasSuffix(name, ".log.gz") {
			archives = append(archives, name)
		}
	}
	sort.Strings(archives)
	return archives, nil
}
