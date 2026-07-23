package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// AlertSettings represents notification configuration
type AlertSettings struct {
	Enabled       bool   `json:"enabled"`
	WebhookURL    string `json:"webhook_url"`
	TelegramToken string `json:"telegram_token"`
	TelegramChatID string `json:"telegram_chat_id"`
	SMTPHost      string `json:"smtp_host"`
	SMTPPort      int    `json:"smtp_port"`
	SMTPUser      string `json:"smtp_user"`
	SMTPPass      string `json:"smtp_pass"`
	SMTPTo        string `json:"smtp_to"`
}

var alertMu sync.RWMutex

// InitAlertsDB creates the database table for alert configurations
func InitAlertsDB() {
	query := `
	CREATE TABLE IF NOT EXISTS alert_settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);`
	_, err := db.Exec(query)
	if err != nil {
		log.Fatalf("[Alerts] alert_settings tablosu oluşturulurken hata: %v", err)
	}

	// Seed defaults if empty
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM alert_settings WHERE key = 'enabled'").Scan(&count)
	if count == 0 {
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('enabled', 'false')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('webhook_url', '')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('telegram_token', '')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('telegram_chat_id', '')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('smtp_host', '')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('smtp_port', '587')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('smtp_user', '')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('smtp_pass', '')")
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES ('smtp_to', '')")
	}
}

// LoadAlertSettings reads current alert configuration from DB
func LoadAlertSettings() AlertSettings {
	alertMu.RLock()
	defer alertMu.RUnlock()

	var cfg AlertSettings
	getVal := func(k string) string {
		var v string
		_ = db.QueryRow("SELECT value FROM alert_settings WHERE key = ?", k).Scan(&v)
		return v
	}

	cfg.Enabled = getVal("enabled") == "true"
	cfg.WebhookURL = getVal("webhook_url")
	cfg.TelegramToken = getVal("telegram_token")
	cfg.TelegramChatID = getVal("telegram_chat_id")
	cfg.SMTPHost = getVal("smtp_host")
	cfg.SMTPPort, _ = strconv.Atoi(getVal("smtp_port"))
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 587
	}
	cfg.SMTPUser = getVal("smtp_user")
	cfg.SMTPPass = getVal("smtp_pass")
	cfg.SMTPTo = getVal("smtp_to")

	return cfg
}

// SaveAlertSettings updates alert configurations in DB
func SaveAlertSettings(cfg AlertSettings) error {
	alertMu.Lock()
	defer alertMu.Unlock()

	setVal := func(k, v string) {
		_, _ = db.Exec("INSERT INTO alert_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?", k, v, v)
	}

	enabledStr := "false"
	if cfg.Enabled {
		enabledStr = "true"
	}
	setVal("enabled", enabledStr)
	setVal("webhook_url", cfg.WebhookURL)
	setVal("telegram_token", cfg.TelegramToken)
	setVal("telegram_chat_id", cfg.TelegramChatID)
	setVal("smtp_host", cfg.SMTPHost)
	setVal("smtp_port", strconv.Itoa(cfg.SMTPPort))
	setVal("smtp_user", cfg.SMTPUser)
	setVal("smtp_pass", cfg.SMTPPass)
	setVal("smtp_to", cfg.SMTPTo)

	return nil
}

// SendAlert asynchronously dispatches an alert across enabled notification channels
func SendAlert(severity string, title string, message string) {
	go func() {
		cfg := LoadAlertSettings()
		if !cfg.Enabled {
			return
		}

		fullMsg := fmt.Sprintf("[%s] %s: %s (Zaman: %s)", strings.ToUpper(severity), title, message, time.Now().Format("2006-01-02 15:04:05"))

		// 1. Webhook Notification
		if cfg.WebhookURL != "" {
			payload := map[string]string{
				"severity":  severity,
				"title":     title,
				"message":   message,
				"timestamp": time.Now().Format(time.RFC3339),
			}
			jsonBytes, _ := json.Marshal(payload)
			client := http.Client{Timeout: 5 * time.Second}
			_, err := client.Post(cfg.WebhookURL, "application/json", bytes.NewBuffer(jsonBytes))
			if err != nil {
				log.Printf("[Alerts] Webhook gönderim hatası: %v", err)
			}
		}

		// 2. Telegram Bot Notification
		if cfg.TelegramToken != "" && cfg.TelegramChatID != "" {
			telegramURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramToken)
			client := http.Client{Timeout: 5 * time.Second}
			formData := url.Values{
				"chat_id": {cfg.TelegramChatID},
				"text":    {fullMsg},
			}
			_, err := client.PostForm(telegramURL, formData)
			if err != nil {
				log.Printf("[Alerts] Telegram gönderim hatası: %v", err)
			}
		}

		// 3. SMTP Email Notification
		if cfg.SMTPHost != "" && cfg.SMTPTo != "" {
			auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
			addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
			body := fmt.Sprintf("To: %s\r\nSubject: BizBox Alert: %s\r\n\r\n%s", cfg.SMTPTo, title, fullMsg)
			err := smtp.SendMail(addr, auth, cfg.SMTPUser, []string{cfg.SMTPTo}, []byte(body))
			if err != nil {
				log.Printf("[Alerts] SMTP email gönderim hatası: %v", err)
			}
		}
	}()
}

// POST /api/alerts/settings
func handlePostAlertSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	enabled := r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on"
	port, _ := strconv.Atoi(r.FormValue("smtp_port"))

	cfg := AlertSettings{
		Enabled:        enabled,
		WebhookURL:     r.FormValue("webhook_url"),
		TelegramToken:  r.FormValue("telegram_token"),
		TelegramChatID: r.FormValue("telegram_chat_id"),
		SMTPHost:       r.FormValue("smtp_host"),
		SMTPPort:       port,
		SMTPUser:       r.FormValue("smtp_user"),
		SMTPPass:       r.FormValue("smtp_pass"),
		SMTPTo:         r.FormValue("smtp_to"),
	}

	if err := SaveAlertSettings(cfg); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(fmt.Sprintf(`<div class="alert alert-danger" style="padding:10px; margin-bottom:15px;">Ayarlar kaydedilemedi: %v</div>`, err)))
		return
	}

	LogSystemEvent(getUsername(r), "Bildirim Ayarları", "Bildirim kanalları güncellendi", "Başarılı")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<div class="alert alert-success" style="padding:10px; background-color:rgba(16,185,129,0.1); color:var(--success-color); border-radius:4px; margin-bottom:15px; font-size:13px;">Bildirim ayarları başarıyla kaydedildi.</div>`))
}

// POST /api/alerts/test
func handlePostAlertTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	SendAlert("info", "Test Uyarısı", "BizBox bildirim altyapısı başarıyla test edildi.")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<div class="alert alert-success" style="padding:10px; background-color:rgba(16,185,129,0.1); color:var(--success-color); border-radius:4px; margin-bottom:15px; font-size:13px;">Test uyarısı gönderildi! Yapılandırılan bildirim kanallarınızı kontrol edin.</div>`))
}
