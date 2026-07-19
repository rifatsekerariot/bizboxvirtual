package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSecurityModule(t *testing.T) {
	// Setup in-memory SQLite database
	var err error
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Test veritabanı bağlantı hatası: %v", err)
	}
	defer db.Close()

	// Initialize security tables
	InitSecurityDB()

	// Test 1: Get Initial Security Status
	req := httptest.NewRequest("GET", "/api/security/status", nil)
	rr := httptest.NewRecorder()

	handleGetSecurityStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/security/status beklenen HTTP 200, alınan: %d", rr.Code)
	}

	var status SecurityStatus
	err = json.Unmarshal(rr.Body.Bytes(), &status)
	if err != nil {
		t.Fatalf("JSON decode hatası: %v", err)
	}

	// Default values seeded are active: true, blocked_count: 0
	if !status.Active {
		t.Errorf("Beklenen active: true, alınan: %t", status.Active)
	}
	if status.BlockedCount != 0 {
		t.Errorf("Beklenen blocked_count: 0, alınan: %d", status.BlockedCount)
	}

	// Test 2: Toggle Security Status (Active -> Inactive)
	req = httptest.NewRequest("POST", "/api/security/toggle", nil)
	rr = httptest.NewRecorder()

	handleToggleSecurity(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("POST /api/security/toggle beklenen HTTP 200, alınan: %d", rr.Code)
	}

	err = json.Unmarshal(rr.Body.Bytes(), &status)
	if err != nil {
		t.Fatalf("JSON decode hatası: %v", err)
	}

	if status.Active {
		t.Errorf("Toggle sonrası beklenen active: false, alınan: %t", status.Active)
	}

	// Verify database was updated
	var activeStr string
	err = db.QueryRow("SELECT value FROM security_settings WHERE key = 'active'").Scan(&activeStr)
	if err != nil || activeStr != "false" {
		t.Errorf("Veritabanı güncellenemedi veya yanlış değer: %s", activeStr)
	}

	// Test 3: Toggle back to Active using HTMX request
	req = httptest.NewRequest("POST", "/api/security/toggle", nil)
	req.Header.Set("HX-Request", "true")
	rr = httptest.NewRecorder()

	handleToggleSecurity(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("HTMX POST /api/security/toggle beklenen HTTP 200, alınan: %d", rr.Code)
	}

	triggerHeader := rr.Header().Get("HX-Trigger")
	if triggerHeader != "security-updated" {
		t.Errorf("Beklenen HX-Trigger header 'security-updated', alınan: %s", triggerHeader)
	}

	// Verify database was updated back to true
	err = db.QueryRow("SELECT value FROM security_settings WHERE key = 'active'").Scan(&activeStr)
	if err != nil || activeStr != "true" {
		t.Errorf("Veritabanı güncellenemedi veya yanlış değer: %s", activeStr)
	}

	// Test 4: Verify logs were written
	var logsCount int
	err = db.QueryRow("SELECT COUNT(*) FROM security_logs").Scan(&logsCount)
	if err != nil {
		t.Fatalf("Log sayımı sorgulama hatası: %v", err)
	}

	// Seeding writes 0 logs, and our 2 toggles write 2 more logs -> total 2
	if logsCount != 2 {
		t.Errorf("Beklenen log sayısı: 2, veritabanındaki: %d", logsCount)
	}
}
