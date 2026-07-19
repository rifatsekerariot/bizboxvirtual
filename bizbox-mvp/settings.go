package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// InitSettingsDB sets up user settings table schema extensions.
func InitSettingsDB() {
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN session_timeout TEXT DEFAULT '24h'")
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN two_factor_enabled INTEGER DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN two_factor_secret TEXT DEFAULT ''")
}

// getSessionDuration parses the duration string.
func getSessionDuration(timeoutStr string) time.Duration {
	d, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}

// GET /api/settings/page
func handleGetSettingsPage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "true" {
		http.Error(w, "Yalnızca HTMX istekleri kabul edilir", http.StatusBadRequest)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, valid := getSessionUser(cookie.Value)
	if !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	dbUser, err := getUserFromDB(user.Username)
	if err != nil {
		dbUser = user
	}

	var qrDataURL string
	var proposedSecret string
	if !dbUser.TwoFactorEnabled {
		// Generate proposed 2FA secret and QR code base64 image
		key, err := totp.Generate(totp.GenerateOpts{
			Issuer:      "BizBox",
			AccountName: dbUser.Username,
		})
		if err == nil {
			proposedSecret = key.Secret()
			var buf bytes.Buffer
			img, err := key.Image(200, 200)
			if err == nil {
				_ = png.Encode(&buf, img)
				qrDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
			}
		}
	}

	// Load version configuration
	versionCfg, _ := loadVersionConfig()

	data := struct {
		User           User
		QRDataURL      string
		ProposedSecret string
		Version        VersionConfig
	}{
		User:           dbUser,
		QRDataURL:      qrDataURL,
		ProposedSecret: proposedSecret,
		Version:        versionCfg,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	err = templates.ExecuteTemplate(w, "settings.html", data)
	if err != nil {
		log.Printf("[Settings] settings.html render hatası: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// POST /api/settings/password
func handlePostSettingsPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, valid := getSessionUser(cookie.Value)
	if !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	oldPassword := r.FormValue("old_password")
	newPassword := r.FormValue("new_password")
	newPasswordConfirm := r.FormValue("new_password_confirm")

	dbUser, err := getUserFromDB(user.Username)
	if err != nil {
		http.Error(w, "Kullanıcı bulunamadı", http.StatusNotFound)
		return
	}

	// Verify old password
	if bcrypt.CompareHashAndPassword([]byte(dbUser.PasswordHash), []byte(oldPassword)) != nil {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Mevcut şifreniz hatalı.</div>`))
		return
	}

	if newPassword != newPasswordConfirm {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Yeni şifreler uyuşmuyor.</div>`))
		return
	}

	if len(newPassword) < 4 {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Yeni şifre en az 4 karakter olmalıdır.</div>`))
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Şifre hashleme hatası", http.StatusInternalServerError)
		return
	}

	_, err = db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(hashedPassword), dbUser.ID)
	if err != nil {
		http.Error(w, "Veritabanı güncelleme hatası", http.StatusInternalServerError)
		return
	}

	// Update cached user's password hash in sessionStore
	sessionMu.Lock()
	if session, exists := sessionStore[cookie.Value]; exists {
		session.User.PasswordHash = string(hashedPassword)
		sessionStore[cookie.Value] = session
	}
	sessionMu.Unlock()

	w.Write([]byte(`<div class="alert alert-success" style="padding: 10px; background-color: rgba(22, 163, 74, 0.1); color: var(--success-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Şifreniz başarıyla güncellendi.</div><script>document.getElementById('old_password').value='';document.getElementById('new_password').value='';document.getElementById('new_password_confirm').value='';</script>`))
}

// POST /api/settings/session
func handlePostSettingsSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, valid := getSessionUser(cookie.Value)
	if !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	timeout := r.FormValue("session_timeout")
	if timeout != "15m" && timeout != "30m" && timeout != "1h" && timeout != "4h" && timeout != "24h" {
		http.Error(w, "Geçersiz oturum süresi değeri", http.StatusBadRequest)
		return
	}

	_, err = db.Exec("UPDATE users SET session_timeout = ? WHERE id = ?", timeout, user.ID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Veritabanı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	// Update current session's user data and expiry
	duration := getSessionDuration(timeout)
	sessionMu.Lock()
	if session, exists := sessionStore[cookie.Value]; exists {
		session.User.SessionTimeout = timeout
		session.ExpiresAt = time.Now().Add(duration)
		sessionStore[cookie.Value] = session
	}
	sessionMu.Unlock()

	// Update the response cookie as well
	newCookie := &http.Cookie{
		Name:     "session_token",
		Value:    cookie.Value,
		Path:     "/",
		Expires:  time.Now().Add(duration),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, newCookie)

	w.Write([]byte(`<div class="alert alert-success" style="padding: 10px; background-color: rgba(22, 163, 74, 0.1); color: var(--success-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Oturum süresi başarıyla güncellendi.</div>`))
}

// POST /api/settings/2fa/enable
func handlePostSettings2FAEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, valid := getSessionUser(cookie.Value)
	if !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	secret := r.FormValue("secret")
	passcode := r.FormValue("passcode")

	secret = strings.TrimSpace(secret)
	passcode = strings.TrimSpace(passcode)

	if secret == "" || passcode == "" {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Kod ve anahtar gereklidir.</div>`))
		return
	}

	// Validate the passcode against the secret
	if !totp.Validate(passcode, secret) {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Doğrulama kodu hatalı.</div>`))
		return
	}

	// Update the database
	_, err = db.Exec("UPDATE users SET two_factor_enabled = 1, two_factor_secret = ? WHERE id = ?", secret, user.ID)
	if err != nil {
		http.Error(w, "Veritabanı güncelleme hatası", http.StatusInternalServerError)
		return
	}

	// Update in-memory session user
	sessionMu.Lock()
	if session, exists := sessionStore[cookie.Value]; exists {
		session.User.TwoFactorEnabled = true
		session.User.TwoFactorSecret = secret
		sessionStore[cookie.Value] = session
	}
	sessionMu.Unlock()

	// Trigger settings update reload
	w.Header().Set("HX-Trigger", "settings-updated")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<div class="alert alert-success" style="padding: 10px; background-color: rgba(22, 163, 74, 0.1); color: var(--success-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">2FA başarıyla etkinleştirildi.</div>`))
}

// POST /api/settings/2fa/disable
func handlePostSettings2FADisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, valid := getSessionUser(cookie.Value)
	if !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	passcode := r.FormValue("passcode")
	passcode = strings.TrimSpace(passcode)

	dbUser, err := getUserFromDB(user.Username)
	if err != nil {
		http.Error(w, "Kullanıcı bulunamadı", http.StatusNotFound)
		return
	}

	if !dbUser.TwoFactorEnabled {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">2FA zaten devre dışı.</div>`))
		return
	}

	// Validate the passcode against the stored secret
	if !totp.Validate(passcode, dbUser.TwoFactorSecret) {
		w.Write([]byte(`<div class="alert alert-danger" style="padding: 10px; background-color: rgba(220, 38, 38, 0.1); color: var(--error-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Doğrulama kodu hatalı.</div>`))
		return
	}

	// Update the database
	_, err = db.Exec("UPDATE users SET two_factor_enabled = 0, two_factor_secret = '' WHERE id = ?", user.ID)
	if err != nil {
		http.Error(w, "Veritabanı güncelleme hatası", http.StatusInternalServerError)
		return
	}

	// Update in-memory session user
	sessionMu.Lock()
	if session, exists := sessionStore[cookie.Value]; exists {
		session.User.TwoFactorEnabled = false
		session.User.TwoFactorSecret = ""
		sessionStore[cookie.Value] = session
	}
	sessionMu.Unlock()

	// Trigger settings update reload
	w.Header().Set("HX-Trigger", "settings-updated")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<div class="alert alert-success" style="padding: 10px; background-color: rgba(22, 163, 74, 0.1); color: var(--success-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">2FA başarıyla devre dışı bırakıldı.</div>`))
}
