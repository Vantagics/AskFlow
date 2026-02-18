package errlog

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetGlobal tears down the package-level singleton so each test starts clean.
func resetGlobal() {
	mu.Lock()
	defer mu.Unlock()
	if global != nil {
		global.close()
		global = nil
	}
}

func TestInitAndLogf(t *testing.T) {
	// Use a temp directory so we don't pollute the real log path.
	dir := t.TempDir()
	resetGlobal()

	// Manually set up the logger pointing at the temp dir.
	path := filepath.Join(dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	global = &errorLogger{
		file: f,
		dir:  dir,
		path: path,
		size: 0,
		buf:  make([]byte, 0, writeBufSize),
	}
	mu.Unlock()
	defer resetGlobal()

	Logf("test message %d", 42)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[ERROR] test message 42") {
		t.Errorf("expected log to contain '[ERROR] test message 42', got: %s", content)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	resetGlobal()

	path := filepath.Join(dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	global = &errorLogger{
		file: f,
		dir:  dir,
		path: path,
		size: maxFileSize - 10, // just under the threshold
		buf:  make([]byte, 0, writeBufSize),
	}
	mu.Unlock()
	defer resetGlobal()

	// This write should push size over maxFileSize and trigger rotation.
	Logf("this message triggers rotation because the size counter is near the limit")

	// After rotation, there should be a .gz archive in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var gzFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log.gz") {
			gzFiles = append(gzFiles, e.Name())
		}
	}
	if len(gzFiles) == 0 {
		t.Fatal("expected at least one .gz archive after rotation, found none")
	}

	// Verify the archive is valid gzip and contains the log line.
	gzPath := filepath.Join(dir, gzFiles[0])
	gf, err := os.Open(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	defer gf.Close()

	gr, err := gzip.NewReader(gf)
	if err != nil {
		t.Fatalf("invalid gzip archive: %v", err)
	}
	defer gr.Close()

	content, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("failed to read gzip content: %v", err)
	}
	if !strings.Contains(string(content), "triggers rotation") {
		t.Errorf("archive content missing expected message, got: %s", string(content))
	}

	// The active log file should now be empty or very small (no leftover data).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 0 {
		t.Errorf("expected active log to be empty after rotation, size=%d", info.Size())
	}
}

func TestPruneArchives(t *testing.T) {
	dir := t.TempDir()

	// Create maxBackups + 3 fake archives.
	for i := 0; i < maxBackups+3; i++ {
		name := filepath.Join(dir, strings.Replace(
			"error-20260101-00000X.log.gz", "X", string(rune('0'+i)), 1))
		os.WriteFile(name, []byte("fake"), 0644)
	}

	l := &errorLogger{dir: dir}
	l.pruneArchives()

	entries, _ := os.ReadDir(dir)
	var remaining int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log.gz") {
			remaining++
		}
	}
	if remaining != maxBackups {
		t.Errorf("expected %d archives after prune, got %d", maxBackups, remaining)
	}
}

func TestLogfBeforeInit(t *testing.T) {
	resetGlobal()
	// Should not panic.
	Logf("this should be silently ignored")
}

func TestCloseIdempotent(t *testing.T) {
	resetGlobal()
	// Should not panic even when called multiple times with no init.
	Close()
	Close()
}
