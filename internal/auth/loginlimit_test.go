package auth

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// setupLoginLimitDB creates an in-memory SQLite database with the required tables.
func setupLoginLimitDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS login_attempts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT NOT NULL,
		ip         TEXT NOT NULL,
		success    INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create login_attempts table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS login_bans (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT NOT NULL DEFAULT '',
		ip         TEXT NOT NULL DEFAULT '',
		reason     TEXT NOT NULL DEFAULT '',
		unlocks_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create login_bans table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLoginLimiter_AllowedWithNoAttempts(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	err := ll.CheckAllowed("admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestLoginLimiter_RecordAttempt(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	ll.RecordAttempt("admin", "127.0.0.1", false)
	ll.RecordAttempt("admin", "127.0.0.1", true)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM login_attempts").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 attempts, got %d", count)
	}
}

func TestLoginLimiter_Rule1_ConsecutiveFailuresLock1Hour(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Record 9 failures — should still be allowed
	for i := 0; i < 9; i++ {
		ll.RecordAttempt("admin", "127.0.0.1", false)
	}
	err := ll.CheckAllowed("admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected allowed after 9 failures, got: %v", err)
	}

	// 10th failure — should be locked
	ll.RecordAttempt("admin", "127.0.0.1", false)
	err = ll.CheckAllowed("admin", "127.0.0.1")
	if err == nil {
		t.Fatal("expected lockout after 10 consecutive failures")
	}
}

func TestLoginLimiter_Rule1_SuccessResetsConsecutive(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Record 9 failures, then 1 success
	for i := 0; i < 9; i++ {
		ll.RecordAttempt("admin", "127.0.0.1", false)
	}
	ll.RecordAttempt("admin", "127.0.0.1", true)

	// Record 9 more failures — should still be allowed (reset by success)
	for i := 0; i < 9; i++ {
		ll.RecordAttempt("admin", "127.0.0.1", false)
	}
	err := ll.CheckAllowed("admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected allowed after success reset, got: %v", err)
	}
}

func TestLoginLimiter_Rule2_DailyFailuresLock(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Record 50 failures today (with successes interspersed to avoid rule 1)
	for i := 0; i < 50; i++ {
		ll.RecordAttempt("admin", "127.0.0.1", false)
		if (i+1)%9 == 0 {
			// Insert a success every 9 failures to reset consecutive counter
			ll.RecordAttempt("admin", "127.0.0.1", true)
		}
	}

	err := ll.CheckAllowed("admin", "127.0.0.1")
	if err == nil {
		t.Fatal("expected lockout after 50 daily failures")
	}
	if err.Error() != "今日密码错误次数过多，当天禁止登录" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoginLimiter_Rule3_IPLock10Days(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Record 100 consecutive failures from same IP (different usernames to avoid rule 2)
	for i := 0; i < 100; i++ {
		username := "user" + string(rune('a'+i%26))
		ll.RecordAttempt(username, "10.0.0.1", false)
	}

	err := ll.CheckAllowed("newuser", "10.0.0.1")
	if err == nil {
		t.Fatal("expected IP lockout after 100 consecutive failures")
	}
}

func TestLoginLimiter_Rule3_IPSuccessResets(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Record 99 failures, then 1 success
	for i := 0; i < 99; i++ {
		ll.RecordAttempt("user", "10.0.0.1", false)
	}
	ll.RecordAttempt("user", "10.0.0.1", true)

	// Record 99 more failures — should not trigger IP ban (reset by success)
	for i := 0; i < 99; i++ {
		ll.RecordAttempt("user2", "10.0.0.1", false)
	}

	err := ll.CheckAllowed("user3", "10.0.0.1")
	if err != nil {
		t.Fatalf("expected allowed after IP success reset, got: %v", err)
	}
}

func TestLoginLimiter_ManualBan(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Add a manual ban for a user
	ll.AddManualBan("baduser", "", "suspicious activity", 24*time.Hour)

	err := ll.CheckAllowed("baduser", "127.0.0.1")
	if err == nil {
		t.Fatal("expected manual ban to block login")
	}
	if err.Error() != "suspicious activity" {
		t.Errorf("unexpected error: %v", err)
	}

	// Different user should not be affected
	err = ll.CheckAllowed("gooduser", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected different user to be allowed, got: %v", err)
	}
}

func TestLoginLimiter_ManualBanIP(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Add a manual ban for an IP
	ll.AddManualBan("", "192.168.1.100", "brute force", 24*time.Hour)

	err := ll.CheckAllowed("anyuser", "192.168.1.100")
	if err == nil {
		t.Fatal("expected manual IP ban to block login")
	}

	// Different IP should not be affected
	err = ll.CheckAllowed("anyuser", "192.168.1.200")
	if err != nil {
		t.Fatalf("expected different IP to be allowed, got: %v", err)
	}
}

func TestLoginLimiter_Unban(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Create 10 consecutive failures to trigger rule 1
	for i := 0; i < 10; i++ {
		ll.RecordAttempt("admin", "127.0.0.1", false)
	}
	err := ll.CheckAllowed("admin", "127.0.0.1")
	if err == nil {
		t.Fatal("expected lockout before unban")
	}

	// Unban the user
	ll.Unban("admin", "")

	// Should be allowed now
	err = ll.CheckAllowed("admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected allowed after unban, got: %v", err)
	}
}

func TestLoginLimiter_UnbanManual(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Add manual ban then unban
	ll.AddManualBan("testuser", "", "test ban", 24*time.Hour)

	err := ll.CheckAllowed("testuser", "127.0.0.1")
	if err == nil {
		t.Fatal("expected ban before unban")
	}

	ll.Unban("testuser", "")

	err = ll.CheckAllowed("testuser", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected allowed after manual unban, got: %v", err)
	}
}

func TestLoginLimiter_UnbanIP(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Add manual IP ban then unban
	ll.AddManualBan("", "10.0.0.5", "ip ban", 24*time.Hour)

	err := ll.CheckAllowed("user", "10.0.0.5")
	if err == nil {
		t.Fatal("expected IP ban before unban")
	}

	ll.Unban("", "10.0.0.5")

	err = ll.CheckAllowed("user", "10.0.0.5")
	if err != nil {
		t.Fatalf("expected allowed after IP unban, got: %v", err)
	}
}

func TestLoginLimiter_ListBans_Empty(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	bans := ll.ListBans()
	if len(bans) != 0 {
		t.Errorf("expected 0 bans, got %d", len(bans))
	}
}

func TestLoginLimiter_ListBans_ManualBan(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	ll.AddManualBan("banneduser", "", "test reason", 24*time.Hour)

	bans := ll.ListBans()
	if len(bans) != 1 {
		t.Fatalf("expected 1 ban, got %d", len(bans))
	}
	if bans[0].Username != "banneduser" {
		t.Errorf("expected username 'banneduser', got %q", bans[0].Username)
	}
	if bans[0].Type != "manual_user" {
		t.Errorf("expected type 'manual_user', got %q", bans[0].Type)
	}
	if !bans[0].IsManual {
		t.Error("expected IsManual to be true")
	}
}

func TestLoginLimiter_ListBans_ConsecutiveFailures(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Create 10 consecutive failures
	for i := 0; i < 10; i++ {
		ll.RecordAttempt("admin", "127.0.0.1", false)
	}

	bans := ll.ListBans()
	found := false
	for _, b := range bans {
		if b.Type == "user_consecutive" && b.Username == "admin" {
			found = true
			if b.FailCount < 10 {
				t.Errorf("expected fail_count >= 10, got %d", b.FailCount)
			}
		}
	}
	if !found {
		t.Error("expected to find a user_consecutive ban for 'admin'")
	}
}

func TestLoginLimiter_CleanOld(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Insert an old record manually
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	db.Exec(`INSERT INTO login_attempts (username, ip, success, created_at) VALUES (?, ?, ?, ?)`,
		"olduser", "1.2.3.4", 0, old)

	// Insert a recent record
	ll.RecordAttempt("newuser", "5.6.7.8", false)

	ll.CleanOld()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM login_attempts").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 remaining attempt after cleanup, got %d", count)
	}
}

func TestLoginLimiter_DifferentUsersIndependent(t *testing.T) {
	db := setupLoginLimitDB(t)
	ll := NewLoginLimiter(db)

	// Lock user1 with 10 consecutive failures
	for i := 0; i < 10; i++ {
		ll.RecordAttempt("user1", "127.0.0.1", false)
	}

	// user1 should be locked
	err := ll.CheckAllowed("user1", "127.0.0.1")
	if err == nil {
		t.Fatal("expected user1 to be locked")
	}

	// user2 should still be allowed
	err = ll.CheckAllowed("user2", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected user2 to be allowed, got: %v", err)
	}
}
