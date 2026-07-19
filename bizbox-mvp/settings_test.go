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

	"github.com/pquerna/otp/totp"
)

func TestSettingsModule(t *testing.T) {
	// Clean up existing test database if exists
	os.Remove("bizbox_test_settings.db")
	defer os.Remove("bizbox_test_settings.db")

	var err error
	db, err = sql.Open("sqlite3", "bizbox_test_settings.db")
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	defer db.Close()

	// Create users table and run settings DB migration
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

	InitSettingsDB()

	// Verify columns exist by reading default admin user
	admin, err := getUserFromDB("admin")
	if err != nil {
		t.Fatalf("getUserFromDB failed: %v", err)
	}
	if admin.SessionTimeout != "24h" {
		t.Errorf("Expected default SessionTimeout to be '24h', got '%s'", admin.SessionTimeout)
	}
	if admin.TwoFactorEnabled {
		t.Error("Expected 2FA to be disabled by default")
	}

	// Create session for admin
	token := createSession(admin)
	cookie := &http.Cookie{Name: "session_token", Value: token}

	// Test case 1: Change password successfully
	formPass := url.Values{}
	formPass.Add("old_password", "admin")
	formPass.Add("new_password", "newpassword")
	formPass.Add("new_password_confirm", "newpassword")

	reqPass := httptest.NewRequest("POST", "/api/settings/password", strings.NewReader(formPass.Encode()))
	reqPass.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqPass.AddCookie(cookie)
	wPass := httptest.NewRecorder()

	handlePostSettingsPassword(wPass, reqPass)

	if wPass.Code != http.StatusOK {
		t.Errorf("Expected status OK (200), got %d", wPass.Code)
	}
	if !strings.Contains(wPass.Body.String(), "başarıyla güncellendi") {
		t.Errorf("Expected body to contain success message, got: %s", wPass.Body.String())
	}

	// Test case 2: Try login with new password
	formLogin := url.Values{}
	formLogin.Add("username", "admin")
	formLogin.Add("password", "newpassword")

	reqLogin := httptest.NewRequest("POST", "/api/login", strings.NewReader(formLogin.Encode()))
	reqLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wLogin := httptest.NewRecorder()

	handlePostLogin(wLogin, reqLogin)

	if wLogin.Code != http.StatusSeeOther {
		t.Errorf("Expected status SeeOther (303) on successful login with new password, got %d", wLogin.Code)
	}

	// Test case 3: Change session timeout
	formSess := url.Values{}
	formSess.Add("session_timeout", "15m")

	reqSess := httptest.NewRequest("POST", "/api/settings/session", strings.NewReader(formSess.Encode()))
	reqSess.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqSess.AddCookie(cookie)
	wSess := httptest.NewRecorder()

	handlePostSettingsSession(wSess, reqSess)

	if wSess.Code != http.StatusOK {
		t.Errorf("Expected status OK (200), got %d", wSess.Code)
	}

	// Verify changed timeout in DB
	adminUpdated, _ := getUserFromDB("admin")
	if adminUpdated.SessionTimeout != "15m" {
		t.Errorf("Expected SessionTimeout to be '15m', got '%s'", adminUpdated.SessionTimeout)
	}

	// Test case 4: Enable 2FA
	secret, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "BizBox",
		AccountName: "admin",
	})
	if err != nil {
		t.Fatalf("Failed to generate OTP secret: %v", err)
	}

	passcode, err := totp.GenerateCode(secret.Secret(), time.Now())
	if err != nil {
		t.Fatalf("Failed to generate passcode: %v", err)
	}

	form2FA := url.Values{}
	form2FA.Add("secret", secret.Secret())
	form2FA.Add("passcode", passcode)

	req2FA := httptest.NewRequest("POST", "/api/settings/2fa/enable", strings.NewReader(form2FA.Encode()))
	req2FA.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2FA.AddCookie(cookie)
	w2FA := httptest.NewRecorder()

	handlePostSettings2FAEnable(w2FA, req2FA)

	if w2FA.Code != http.StatusOK {
		t.Errorf("Expected status OK (200), got %d", w2FA.Code)
	}

	// Verify 2FA status in DB
	admin2FAEnabled, _ := getUserFromDB("admin")
	if !admin2FAEnabled.TwoFactorEnabled {
		t.Error("Expected 2FA to be enabled in DB")
	}
	if admin2FAEnabled.TwoFactorSecret != secret.Secret() {
		t.Errorf("Expected secret %s, got %s", secret.Secret(), admin2FAEnabled.TwoFactorSecret)
	}

	// Test case 5: Disable 2FA
	passcodeDisable, _ := totp.GenerateCode(secret.Secret(), time.Now())
	formDisable := url.Values{}
	formDisable.Add("passcode", passcodeDisable)

	reqDisable := httptest.NewRequest("POST", "/api/settings/2fa/disable", strings.NewReader(formDisable.Encode()))
	reqDisable.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDisable.AddCookie(cookie)
	wDisable := httptest.NewRecorder()

	handlePostSettings2FADisable(wDisable, reqDisable)

	if wDisable.Code != http.StatusOK {
		t.Errorf("Expected status OK (200) on disable, got %d", wDisable.Code)
	}

	// Verify 2FA disabled in DB
	admin2FADisabled, _ := getUserFromDB("admin")
	if admin2FADisabled.TwoFactorEnabled {
		t.Error("Expected 2FA to be disabled in DB")
	}
}
