package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp/totp"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

// User represents a user record in the SQLite database.
type User struct {
	ID               int
	Username         string
	PasswordHash     string
	Role             string // "admin", "operator", "viewer"
	CreatedAt        time.Time
	SessionTimeout   string
	TwoFactorEnabled bool
	TwoFactorSecret  string
}

// SessionData stores session info in the in-memory store.
type SessionData struct {
	User      User
	ExpiresAt time.Time
}

type LoginAttempt struct {
	Count       int
	LockedUntil time.Time
}

var (
	db            *sql.DB
	sessionStore  = make(map[string]SessionData)
	sessionMu     sync.RWMutex
	loginAttempts = make(map[string]*LoginAttempt)
	attemptMu     sync.Mutex
)

// InitDB initializes the SQLite database and creates the users table if it doesn't exist.
func InitDB() {
	var err error
	db, err = sql.Open("sqlite3", "bizbox.db")
	if err != nil {
		log.Fatalf("Veritabanı bağlantı hatası: %v", err)
	}

	query := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE,
		password_hash TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = db.Exec(query)
	if err != nil {
		log.Fatalf("Tablo oluşturma hatası: %v", err)
	}

	// Schema migrations for users table
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN role TEXT DEFAULT 'admin'")

	queryLogs := `
	CREATE TABLE IF NOT EXISTS system_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		user TEXT,
		action TEXT,
		target TEXT,
		status TEXT
	);`
	_, err = db.Exec(queryLogs)
	if err != nil {
		log.Fatalf("system_logs tablosu oluşturma hatası: %v", err)
	}

	queryAttempts := `
	CREATE TABLE IF NOT EXISTS login_attempts (
		key TEXT PRIMARY KEY,
		attempt_count INTEGER DEFAULT 0,
		locked_until DATETIME,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, _ = db.Exec(queryAttempts)

	// Seed the admin user
	if err := SeedAdminUser(); err != nil {
		log.Printf("Admin kullanıcı seed hatası: %v", err)
	}
}

// SeedAdminUser simulates the creation of an admin user from the first setup.
func SeedAdminUser() error {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		return err
	}

	// If there are no users, create the default admin user
	if count == 0 {
		username := "admin"
		password := "admin" // Modül 1 TUI default admin password

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}

		_, err = db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, string(hashedPassword))
		if err != nil {
			return err
		}
		log.Printf("[SEED] Default admin kullanıcısı oluşturuldu. Kullanıcı adı: %s, Şifre: %s", username, password)
	}

	return nil
}

// generateSessionToken generates a secure random string to use as session token.
func generateSessionToken() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// createSession creates a new session in the store and returns the token.
func createSession(user User) string {
	token := generateSessionToken()
	sessionMu.Lock()
	defer sessionMu.Unlock()

	timeout := "24h"
	if user.SessionTimeout != "" {
		timeout = user.SessionTimeout
	}
	duration := getSessionDuration(timeout)

	sessionStore[token] = SessionData{
		User:      user,
		ExpiresAt: time.Now().Add(duration),
	}
	return token
}

// getSessionUser returns the user associated with the token if the session is valid and not expired.
func getSessionUser(token string) (User, bool) {
	sessionMu.RLock()
	defer sessionMu.RUnlock()

	data, exists := sessionStore[token]
	if !exists {
		return User{}, false
	}

	if time.Now().After(data.ExpiresAt) {
		// Clean up expired session
		go func() {
			sessionMu.Lock()
			delete(sessionStore, token)
			sessionMu.Unlock()
		}()
		return User{}, false
	}

	return data.User, true
}

// AuthMiddleware wraps protected handlers and checks for a valid session.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static files are exempt from auth checks
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		// Public pages/endpoints
		if r.URL.Path == "/login" || r.URL.Path == "/api/login" {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie("session_token")
		if err != nil {
			handleUnauthorized(w, r)
			return
		}

		_, valid := getSessionUser(cookie.Value)
		if !valid {
			handleUnauthorized(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleUnauthorized redirects users to the login page or returns a 401 response for API/HTMX requests.
func handleUnauthorized(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleGetLogin renders the login page.
func handleGetLogin(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to index
	if cookie, err := r.Cookie("session_token"); err == nil {
		if _, valid := getSessionUser(cookie.Value); valid {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.ExecuteTemplate(w, "login.html", nil)
}

// getUserFromDB retrieves user details including settings and role
func getUserFromDB(username string) (User, error) {
	var user User
	var createdAtStr string
	var sessionTimeout sql.NullString
	var twoFactorEnabled sql.NullInt64
	var twoFactorSecret sql.NullString
	var role sql.NullString

	err := db.QueryRow("SELECT id, username, password_hash, role, created_at, session_timeout, two_factor_enabled, two_factor_secret FROM users WHERE username = ?", username).
		Scan(&user.ID, &user.Username, &user.PasswordHash, &role, &createdAtStr, &sessionTimeout, &twoFactorEnabled, &twoFactorSecret)

	if err != nil {
		err = db.QueryRow("SELECT id, username, password_hash, created_at FROM users WHERE username = ?", username).
			Scan(&user.ID, &user.Username, &user.PasswordHash, &createdAtStr)
		if err != nil {
			return User{}, err
		}
		user.Role = "admin"
		user.SessionTimeout = "24h"
		user.TwoFactorEnabled = false
		user.TwoFactorSecret = ""
	} else {
		if role.Valid && role.String != "" {
			user.Role = role.String
		} else {
			user.Role = "admin"
		}
		if sessionTimeout.Valid {
			user.SessionTimeout = sessionTimeout.String
		} else {
			user.SessionTimeout = "24h"
		}
		user.TwoFactorEnabled = twoFactorEnabled.Valid && twoFactorEnabled.Int64 == 1
		if twoFactorSecret.Valid {
			user.TwoFactorSecret = twoFactorSecret.String
		} else {
			user.TwoFactorSecret = ""
		}
	}

	if t, err := time.Parse("2006-01-02 15:04:05", createdAtStr); err == nil {
		user.CreatedAt = t
	} else if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
		user.CreatedAt = t
	}

	return user, nil
}

// checkAttemptLocked checks if a persistent attempt key is currently locked in SQLite DB
func checkAttemptLocked(key string) (bool, time.Time) {
	var lockedUntilStr sql.NullString
	err := db.QueryRow("SELECT locked_until FROM login_attempts WHERE key = ?", key).Scan(&lockedUntilStr)
	if err != nil || !lockedUntilStr.Valid {
		return false, time.Time{}
	}

	lockedUntil, err := time.Parse(time.RFC3339, lockedUntilStr.String)
	if err != nil {
		lockedUntil, err = time.Parse("2006-01-02 15:04:05", lockedUntilStr.String)
	}
	if err == nil && time.Now().Before(lockedUntil) {
		return true, lockedUntil
	}
	return false, time.Time{}
}

// recordFailedAttempt increments attempt count in DB and sets lockout timestamp if threshold exceeded
func recordFailedAttempt(key string, maxAttempts int, lockDuration time.Duration) (int, bool) {
	var count int
	err := db.QueryRow("SELECT attempt_count FROM login_attempts WHERE key = ?", key).Scan(&count)
	if err != nil {
		count = 0
	}
	count++

	var lockedUntilStr string
	isLocked := false
	if count >= maxAttempts {
		isLocked = true
		lockedUntilStr = time.Now().Add(lockDuration).Format(time.RFC3339)
		_, _ = db.Exec("INSERT INTO login_attempts (key, attempt_count, locked_until, updated_at) VALUES (?, ?, ?, datetime('now')) ON CONFLICT(key) DO UPDATE SET attempt_count = ?, locked_until = ?, updated_at = datetime('now')", key, count, lockedUntilStr, count, lockedUntilStr)
	} else {
		_, _ = db.Exec("INSERT INTO login_attempts (key, attempt_count, updated_at) VALUES (?, ?, datetime('now')) ON CONFLICT(key) DO UPDATE SET attempt_count = ?, updated_at = datetime('now')", key, count, count)
	}

	return count, isLocked
}

// resetFailedAttempt clears persistent failed attempts for a key
func resetFailedAttempt(key string) {
	_, _ = db.Exec("DELETE FROM login_attempts WHERE key = ?", key)
}

// handlePostLogin authenticates the user with persistent brute-force protection and sets a session cookie.
func handlePostLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	twoFactorCode := r.FormValue("two_factor_code")

	clientIP := strings.Split(r.RemoteAddr, ":")[0]
	userKey := "user:" + username
	ipKey := "ip:" + clientIP

	// 1. Check Username Lockout Policy (5 failed attempts per specific username)
	if locked, _ := checkAttemptLocked(userKey); locked {
		data := struct {
			Error    string
			Show2FA  bool
			Username string
			Password string
		}{
			Error: fmt.Sprintf("'%s' kullanıcısı çok sayıda hatalı şifre denemesi nedeniyle kilitlendi. Lütfen 15 dakika bekleyin.", username),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		templates.ExecuteTemplate(w, "login.html", data)
		return
	}

	// 2. Check Global IP Rate Limit Policy (20 failed attempts per IP across any user for NAT safety)
	if locked, _ := checkAttemptLocked(ipKey); locked {
		data := struct {
			Error    string
			Show2FA  bool
			Username string
			Password string
		}{
			Error: fmt.Sprintf("IP adresiniz (%s) çok sayıda hatalı deneme nedeniyle kilitlendi. Lütfen 15 dakika bekleyin.", clientIP),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		templates.ExecuteTemplate(w, "login.html", data)
		return
	}

	user, err := getUserFromDB(username)
	var valid bool
	if err == nil {
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil {
			valid = true
		}
	}

	if !valid {
		// Record failed attempt for specific user account (5 max threshold)
		_, userNowLocked := recordFailedAttempt(userKey, 5, 15*time.Minute)
		if userNowLocked {
			SendAlert("warning", "Kullanıcı Hesabı Kilitlendi", fmt.Sprintf("'%s' kullanıcısı %s IP'sinden 5 kez hatalı şifre girildiği için 15 dakika kilitlendi.", username, clientIP))
		}

		// Record failed attempt for client IP (20 max threshold for NAT/Shared Wi-Fi safety)
		_, ipNowLocked := recordFailedAttempt(ipKey, 20, 15*time.Minute)
		if ipNowLocked {
			SendAlert("warning", "IP Adresi Kilitlendi", fmt.Sprintf("%s IP adresi 20 kez üst üste hatalı giriş denemesi nedeniyle kilitlendi.", clientIP))
		}

		data := struct {
			Error    string
			Show2FA  bool
			Username string
			Password string
		}{
			Error: "Kullanıcı adı veya şifre hatalı",
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		templates.ExecuteTemplate(w, "login.html", data)
		return
	}

	// Login successful: reset failed attempt counter for both user and IP keys
	resetFailedAttempt(userKey)
	resetFailedAttempt(ipKey)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		templates.ExecuteTemplate(w, "login.html", data)
		return
	}

	// Password is valid! Now check 2FA if enabled
	if user.TwoFactorEnabled {
		if twoFactorCode == "" {
			data := struct {
				Show2FA  bool
				Username string
				Password string
				Error    string
			}{
				Show2FA:  true,
				Username: username,
				Password: password,
				Error:    "Lütfen iki adımlı doğrulama (2FA) kodunu girin.",
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			templates.ExecuteTemplate(w, "login.html", data)
			return
		}

		// Validate 2FA passcode
		if !totp.Validate(twoFactorCode, user.TwoFactorSecret) {
			data := struct {
				Show2FA  bool
				Username string
				Password string
				Error    string
			}{
				Show2FA:  true,
				Username: username,
				Password: password,
				Error:    "Girdiğiniz iki adımlı doğrulama kodu hatalı.",
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			templates.ExecuteTemplate(w, "login.html", data)
			return
		}
	}

	// Create session
	token := createSession(user)

	timeout := "24h"
	if user.SessionTimeout != "" {
		timeout = user.SessionTimeout
	}
	duration := getSessionDuration(timeout)

	// Set HttpOnly, Secure, SameSite=Strict cookie
	cookie := &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(duration),
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, cookie)

	// Redirect to dashboard page
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout logs out the user by deleting the session and expiring the cookie.
func handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		sessionMu.Lock()
		delete(sessionStore, cookie.Value)
		sessionMu.Unlock()
	}

	// Expire the cookie
	newCookie := &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, newCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// LogSystemEvent writes an audit log to system_logs table
func LogSystemEvent(user, action, target, status string) {
	_, err := db.Exec("INSERT INTO system_logs (user, action, target, status) VALUES (?, ?, ?, ?)", user, action, target, status)
	if err != nil {
		log.Printf("System log yazma hatası: %v", err)
	}
}

// getUsername retrieves the logged in user's username from session cookie
func getUsername(r *http.Request) string {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		if u, valid := getSessionUser(cookie.Value); valid {
			return u.Username
		}
	}
	return "system"
}
