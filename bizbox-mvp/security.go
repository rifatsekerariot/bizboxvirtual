package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
)

// SecurityStatus represents the security module state
type SecurityStatus struct {
	Active       bool `json:"active"`
	BlockedCount int  `json:"blocked_count"`
}

// SecurityPageData is passed to the security.html template
type SecurityPageData struct {
	Active       bool
	BlockedCount int
	Logs         []SecurityLog
}

// SecurityLog holds individual block events
type SecurityLog struct {
	ID        int
	Timestamp string
	Action    string
}

// InitSecurityDB sets up the database table and seeds initial settings
func InitSecurityDB() {
	querySettings := `
	CREATE TABLE IF NOT EXISTS security_settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);`
	_, err := db.Exec(querySettings)
	if err != nil {
		log.Fatalf("security_settings tablosu oluşturulurken hata: %v", err)
	}

	queryLogs := `
	CREATE TABLE IF NOT EXISTS security_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		action TEXT
	);`
	_, err = db.Exec(queryLogs)
	if err != nil {
		log.Fatalf("security_logs tablosu oluşturulurken hata: %v", err)
	}

	// Seed status if empty
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM security_settings WHERE key = 'active'").Scan(&count)
	if err == nil && count == 0 {
		_, _ = db.Exec("INSERT INTO security_settings (key, value) VALUES ('active', 'true')")
		_, _ = db.Exec("INSERT INTO security_settings (key, value) VALUES ('blocked_count', '0')")
	}
}

// GET /api/security/status
func handleGetSecurityStatus(w http.ResponseWriter, r *http.Request) {
	var activeStr string
	var blockedCount int

	err := db.QueryRow("SELECT value FROM security_settings WHERE key = 'active'").Scan(&activeStr)
	if err != nil {
		activeStr = "false"
	}
	err = db.QueryRow("SELECT value FROM security_settings WHERE key = 'blocked_count'").Scan(&blockedCount)
	if err != nil {
		blockedCount = 0
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SecurityStatus{
		Active:       activeStr == "true",
		BlockedCount: blockedCount,
	})
}

// POST /api/security/toggle
func handleToggleSecurity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Yalnızca POST istekleri desteklenir", http.StatusMethodNotAllowed)
		return
	}

	var activeStr string
	err := db.QueryRow("SELECT value FROM security_settings WHERE key = 'active'").Scan(&activeStr)
	if err != nil {
		activeStr = "false"
	}

	newActive := "true"
	if activeStr == "true" {
		newActive = "false"
	}

	_, err = db.Exec("UPDATE security_settings SET value = ? WHERE key = 'active'", newActive)
	if err != nil {
		http.Error(w, fmt.Sprintf("Veritabanı güncelleme hatası: %v", err), http.StatusInternalServerError)
		return
	}

	var cmd *exec.Cmd
	logMsg := "XDP Saldırı Koruması devredışı bırakıldı (systemctl stop xdp-ddos.service)"
	if newActive == "true" {
		logMsg = "XDP Saldırı Koruması etkinleştirildi (systemctl start xdp-ddos.service)"
		cmd = exec.Command("systemctl", "start", "xdp-ddos.service")
	} else {
		cmd = exec.Command("systemctl", "stop", "xdp-ddos.service")
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("XDP servis işlemi başarısız: %v. Detay: %s", err, string(out)), http.StatusInternalServerError)
		return
	}

	_, _ = db.Exec("INSERT INTO security_logs (timestamp, action) VALUES (datetime('now', 'localtime'), ?)", logMsg)

	// Return updated state
	var blockedCount int
	err = db.QueryRow("SELECT value FROM security_settings WHERE key = 'blocked_count'").Scan(&blockedCount)
	if err != nil {
		blockedCount = 0
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "security-updated")
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SecurityStatus{
		Active:       newActive == "true",
		BlockedCount: blockedCount,
	})
}

// GET /api/security/page
func handleGetSecurityPage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "true" {
		http.Error(w, "Yalnızca HTMX istekleri kabul edilir", http.StatusBadRequest)
		return
	}

	var activeStr string
	var blockedCount int

	err := db.QueryRow("SELECT value FROM security_settings WHERE key = 'active'").Scan(&activeStr)
	if err != nil {
		activeStr = "false"
	}
	err = db.QueryRow("SELECT value FROM security_settings WHERE key = 'blocked_count'").Scan(&blockedCount)
	if err != nil {
		blockedCount = 0
	}

	// Fetch logs
	rows, err := db.Query("SELECT id, timestamp, action FROM security_logs ORDER BY id DESC LIMIT 50")
	var logs []SecurityLog
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var l SecurityLog
			if err := rows.Scan(&l.ID, &l.Timestamp, &l.Action); err == nil {
				logs = append(logs, l)
			}
		}
	}

	data := SecurityPageData{
		Active:       activeStr == "true",
		BlockedCount: blockedCount,
		Logs:         logs,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	err = templates.ExecuteTemplate(w, "security.html", data)
	if err != nil {
		log.Printf("[Security] security.html render hatası: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
