package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpdatesModule(t *testing.T) {
	// Backup original config path and change it to a test path
	oldConfigPath := configPath
	configPath = "config/version_test.json"
	defer func() {
		configPath = oldConfigPath
		os.Remove("config/version_test.json")
	}()

	// Set up initial test config
	testCfg := VersionConfig{
		CurrentVersion: "v1.0.0",
		NewVersion:     "v1.1.0",
		HasUpdate:      true,
		Changelog:      "Yedekleme geliştirmeleri.",
	}
	data, _ := json.Marshal(testCfg)
	_ = os.WriteFile(configPath, data, 0644)

	// Test 1: handleGetUpdatesCheck
	reqCheck := httptest.NewRequest("GET", "/api/updates/check", nil)
	wCheck := httptest.NewRecorder()
	handleGetUpdatesCheck(wCheck, reqCheck)

	if wCheck.Code != http.StatusOK {
		t.Errorf("Expected check code 200, got %d", wCheck.Code)
	}

	var checkedCfg VersionConfig
	if err := json.Unmarshal(wCheck.Body.Bytes(), &checkedCfg); err != nil {
		t.Fatalf("Failed to parse check response: %v", err)
	}
	if checkedCfg.CurrentVersion != "v1.0.0" || !checkedCfg.HasUpdate {
		t.Errorf("Expected current version v1.0.0 and has_update=true, got: %+v", checkedCfg)
	}

	// Test 2: handleGetUpdateStatus initially (idle)
	reqStatusIdle := httptest.NewRequest("GET", "/api/updates/status", nil)
	wStatusIdle := httptest.NewRecorder()
	handleGetUpdateStatus(wStatusIdle, reqStatusIdle)

	if wStatusIdle.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", wStatusIdle.Code)
	}
	if !strings.Contains(wStatusIdle.Body.String(), "Şimdi Güncelle") {
		t.Errorf("Expected idle status layout with 'Şimdi Güncelle' button, got: %s", wStatusIdle.Body.String())
	}

	// Test 3: Run simulated update (Success path)
	// We'll reset state first
	updateMu.Lock()
	updateState = UpdateState{
		Status:   "idle",
		Progress: 0,
		Message:  "Sistem güncel.",
	}
	updateMu.Unlock()

	reqStartSuccess := httptest.NewRequest("POST", "/api/updates/start", nil)
	wStartSuccess := httptest.NewRecorder()
	handleStartUpdate(wStartSuccess, reqStartSuccess)

	if wStartSuccess.Code != http.StatusOK {
		t.Errorf("Expected start status 200, got %d", wStartSuccess.Code)
	}
	
	// Wait for goroutine to finish (approx 6 seconds in simulation)
	// We will poll updates/status until it's not "running"
	success := false
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		updateMu.Lock()
		currState := updateState
		updateMu.Unlock()

		if currState.Status == "success" {
			success = true
			break
		}
		if currState.Status == "failed" {
			t.Fatalf("Update failed during success path test: %s - %s", currState.Message, currState.ErrorMsg)
		}
	}

	if !success {
		t.Error("Update simulation timed out or failed to reach success status")
	}

	// Check if version.json updated
	updatedCfg, err := loadVersionConfig()
	if err != nil {
		t.Fatalf("Failed to load version config after success: %v", err)
	}
	if updatedCfg.CurrentVersion != "v1.1.0" || updatedCfg.HasUpdate {
		t.Errorf("Expected version to be updated to v1.1.0 with has_update=false, got: %+v", updatedCfg)
	}

	// Reset config for Failure path test
	_ = os.WriteFile(configPath, data, 0644)

	// Test 4: Run simulated update (Failure / Rollback path)
	// We'll reset state first
	reqReset := httptest.NewRequest("POST", "/api/updates/reset", nil)
	wReset := httptest.NewRecorder()
	handleResetUpdate(wReset, reqReset)

	formErr := url.Values{}
	formErr.Add("simulate_error", "true")
	reqStartErr := httptest.NewRequest("POST", "/api/updates/start", strings.NewReader(formErr.Encode()))
	reqStartErr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wStartErr := httptest.NewRecorder()
	
	handleStartUpdate(wStartErr, reqStartErr)

	if wStartErr.Code != http.StatusOK {
		t.Errorf("Expected start error status 200, got %d", wStartErr.Code)
	}

	failed := false
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		updateMu.Lock()
		currState := updateState
		updateMu.Unlock()

		if currState.Status == "failed" {
			failed = true
			if !strings.Contains(currState.Message, "başarısız") {
				t.Errorf("Expected failed message to contain fail indicator, got: %s", currState.Message)
			}
			break
		}
		if currState.Status == "success" {
			t.Fatal("Update succeeded unexpectedly during failure simulation test")
		}
	}

	if !failed {
		t.Error("Update simulation timed out or failed to reach failed/rollback status")
	}

	// Verify that version config was NOT updated (current_version should remain v1.0.0)
	notUpdatedCfg, _ := loadVersionConfig()
	if notUpdatedCfg.CurrentVersion != "v1.0.0" || !notUpdatedCfg.HasUpdate {
		t.Errorf("Expected version to remain v1.0.0 with has_update=true after failed update, got: %+v", notUpdatedCfg)
	}
}
