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

// handlePostLogin authenticates the user with brute-force protection and sets a session cookie.
func handlePostLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	twoFactorCode := r.FormValue("two_factor_code")

	attemptKey := r.RemoteAddr + ":" + username
	attemptMu.Lock()
	attempt, exists := loginAttempts[attemptKey]
	if exists && time.Now().Before(attempt.LockedUntil) {
		attemptMu.Unlock()
		data := struct {
			Error    string
			Show2FA  bool
			Username string
			Password string
		}{
			Error: "Çok sayıda hatalı giriş denemesi. Hesabınız 15 dakika boyunca kilitlendi.",
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		templates.ExecuteTemplate(w, "login.html", data)
		return
	}
	attemptMu.Unlock()

	user, err := getUserFromDB(username)
	var valid bool
	if err == nil {
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil {
			valid = true
		}
	}

	if !valid {
		attemptMu.Lock()
		if !exists {
			attempt = &LoginAttempt{Count: 0}
			loginAttempts[attemptKey] = attempt
		}
		attempt.Count++
		if attempt.Count >= 5 {
			attempt.LockedUntil = time.Now().Add(15 * time.Minute)
			SendAlert("warning", "Brute-force Giriş Uyarısı", fmt.Sprintf("%s IP adresinden '%s' kullanıcısı için 5 kez üst üste hatalı giriş yapıldı. Hesap 15 dakika kilitlendi.", r.RemoteAddr, username))
		}
		attemptMu.Unlock()

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

	// Login successful: reset failed attempt counter
	attemptMu.Lock()
	delete(loginAttempts, attemptKey)
	attemptMu.Unlock()
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
