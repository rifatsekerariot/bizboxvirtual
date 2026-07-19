package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashing(t *testing.T) {
	password := "supersecret"
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Password hashing failed: %v", err)
	}

	err = bcrypt.CompareHashAndPassword(hashed, []byte(password))
	if err != nil {
		t.Errorf("Password verification failed: %v", err)
	}

	err = bcrypt.CompareHashAndPassword(hashed, []byte("wrongpassword"))
	if err == nil {
		t.Errorf("Password verification should fail for incorrect password")
	}
}

func TestSessionManagement(t *testing.T) {
	user := User{
		ID:        1,
		Username:  "testuser",
		CreatedAt: time.Now(),
	}

	token := createSession(user)
	if token == "" {
		t.Fatal("Failed to create session token")
	}

	retrievedUser, valid := getSessionUser(token)
	if !valid {
		t.Fatalf("Expected valid session for token %s", token)
	}

	if retrievedUser.Username != user.Username {
		t.Errorf("Expected username %s, got %s", user.Username, retrievedUser.Username)
	}

	// Test invalid token
	_, valid = getSessionUser("invalid_token")
	if valid {
		t.Error("Expected invalid session for dummy token")
	}

	// Test expired session
	sessionMu.Lock()
	sessionStore[token] = SessionData{
		User:      user,
		ExpiresAt: time.Now().Add(-1 * time.Second), // expired
	}
	sessionMu.Unlock()

	_, valid = getSessionUser(token)
	if valid {
		t.Error("Expected expired session to be invalid")
	}
}

func TestDatabaseInitAndSeed(t *testing.T) {
	// Clean up existing test database if exists
	os.Remove("bizbox_test.db")
	defer os.Remove("bizbox_test.db")

	var err error
	db, err = sql.Open("sqlite3", "bizbox_test.db")
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	defer db.Close()

	query := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE,
		password_hash TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = db.Exec(query)
	if err != nil {
		t.Fatalf("Failed to create test users table: %v", err)
	}

	err = SeedAdminUser()
	if err != nil {
		t.Fatalf("SeedAdminUser failed: %v", err)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query user count: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 seeded user, got %d", count)
	}

	var username, passwordHash string
	err = db.QueryRow("SELECT username, password_hash FROM users").Scan(&username, &passwordHash)
	if err != nil {
		t.Fatalf("Failed to query user details: %v", err)
	}

	if username != "admin" {
		t.Errorf("Expected username 'admin', got '%s'", username)
	}

	err = bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte("admin"))
	if err != nil {
		t.Errorf("Seeded admin password mismatch: %v", err)
	}
}

func TestAuthMiddleware(t *testing.T) {
	// Setup standard mux
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Dashboard Content"))
	})
	mux.HandleFunc("GET /api/vms", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	})

	handler := AuthMiddleware(mux)

	// Case 1: Unauthorized request to Dashboard (HTML)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status SeeOther (303) on unauthorized page request, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "/login") {
		t.Errorf("Expected redirect to /login, got Location: %s", w.Header().Get("Location"))
	}

	// Case 2: Unauthorized request to API
	req = httptest.NewRequest("GET", "/api/vms", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status Unauthorized (401) on unauthorized API request, got %d", w.Code)
	}

	// Case 3: Unauthorized request to HTMX
	req = httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401 on unauthorized HTMX request, got %d", w.Code)
	}
	if w.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("Expected HX-Redirect header to /login, got '%s'", w.Header().Get("HX-Redirect"))
	}

	// Case 4: Authorized request with valid session token
	testUser := User{
		ID:       2,
		Username: "admin",
	}
	token := createSession(testUser)

	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  "session_token",
		Value: token,
	})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status OK (200) for authorized request, got %d", w.Code)
	}
	if w.Body.String() != "Dashboard Content" {
		t.Errorf("Expected body 'Dashboard Content', got '%s'", w.Body.String())
	}
}

func TestLoginEndpoint(t *testing.T) {
	// Clean up existing test database if exists
	os.Remove("bizbox_test.db")
	defer os.Remove("bizbox_test.db")

	var err error
	db, err = sql.Open("sqlite3", "bizbox_test.db")
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	defer db.Close()

	query := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE,
		password_hash TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = db.Exec(query)
	if err != nil {
		t.Fatalf("Failed to create test users table: %v", err)
	}

	err = SeedAdminUser()
	if err != nil {
		t.Fatalf("SeedAdminUser failed: %v", err)
	}

	// Case 1: Post Login with valid credentials
	form := url.Values{}
	form.Add("username", "admin")
	form.Add("password", "admin")

	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	handlePostLogin(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status SeeOther (303) on successful login, got %d", w.Code)
	}

	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "session_token" {
			sessionCookie = c
			break
		}
	}

	if sessionCookie == nil {
		t.Fatal("Expected session_token cookie to be set")
	}

	if !sessionCookie.HttpOnly {
		t.Error("Expected session cookie to have HttpOnly flag set")
	}

	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("Expected session cookie same site to be Strict, got %v", sessionCookie.SameSite)
	}

	// Case 2: Post Login with invalid credentials
	form = url.Values{}
	form.Add("username", "admin")
	form.Add("password", "wrongpass")

	req = httptest.NewRequest("POST", "/api/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()

	handlePostLogin(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status Unauthorized (401) on invalid credentials login, got %d", w.Code)
	}
}
