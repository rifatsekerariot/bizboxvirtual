package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestQoSRulesAndInheritance(t *testing.T) {
	// Setup in-memory SQLite database
	var err error
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Test veritabanı bağlantı hatası: %v", err)
	}
	defer db.Close()

	// Initialize tables
	InitNetworkDB()
	InitQosDB()

	// Test 1: Setting segment priority
	err = SetPriority("Muhasebe", "high")
	if err != nil {
		t.Fatalf("Segment önceliği ayarlanamadı: %v", err)
	}

	rules := GetQoSRulesMap()
	if rules["Muhasebe"] != "high" {
		t.Errorf("Muhasebe segmenti için beklenen öncelik 'high', alınan: %s", rules["Muhasebe"])
	}

	// Test 2: VM inheritance of segment priority
	// Assign vm-test-1 to Muhasebe
	err = AssignVMToSegment("vm-test-1", "Muhasebe")
	if err != nil {
		t.Fatalf("VM segmente atanamadı: %v", err)
	}

	// Effective priority should be high (inherited from Muhasebe)
	var priority string
	err = db.QueryRow("SELECT priority FROM qos_rules WHERE target = 'Muhasebe'").Scan(&priority)
	if err != nil || priority != "high" {
		t.Errorf("Segment kuralı sorgulama hatası veya beklenmedik değer: %s", priority)
	}

	// Test 3: Direct VM priority overrides inherited segment priority
	err = SetPriority("vm-test-1", "low")
	if err != nil {
		t.Fatalf("VM önceliği ayarlanamadı: %v", err)
	}

	// Since we set direct rule on vm-test-1, checking qos_rules should show "low" for vm-test-1
	err = db.QueryRow("SELECT priority FROM qos_rules WHERE target = 'vm-test-1'").Scan(&priority)
	if err != nil || priority != "low" {
		t.Errorf("VM doğrudan kuralı sorgulama hatası veya beklenmedik değer: %s", priority)
	}
}

func TestQoSAPI(t *testing.T) {
	// Setup in-memory SQLite database
	var err error
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Test veritabanı bağlantı hatası: %v", err)
	}
	defer db.Close()

	InitNetworkDB()
	InitQosDB()

	// Set a mock rule
	err = SetPriority("Muhasebe", "high")
	if err != nil {
		t.Fatalf("SetPriority hatası: %v", err)
	}

	// 1. Test GET /api/qos/rules
	req := httptest.NewRequest("GET", "/api/qos/rules", nil)
	rr := httptest.NewRecorder()

	handleGetQoSRules(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/qos/rules beklenen HTTP 200, alınan: %d", rr.Code)
	}

	type QoSRule struct {
		Target   string `json:"target"`
		Priority string `json:"priority"`
	}

	var rules []QoSRule
	err = json.Unmarshal(rr.Body.Bytes(), &rules)
	if err != nil {
		t.Fatalf("JSON decode hatası: %v", err)
	}

	if len(rules) != 1 || rules[0].Target != "Muhasebe" || rules[0].Priority != "high" {
		t.Errorf("Beklenmedik kurallar listesi: %v", rules)
	}

	// 2. Test POST /api/qos/rules (JSON API request)
	body := `{"target": "Misafir Wifi", "priority": "low"}`
	req = httptest.NewRequest("POST", "/api/qos/rules", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()

	handleCreateQoSRule(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("POST /api/qos/rules beklenen HTTP 200, alınan: %d", rr.Code)
	}

	// Verify rule added
	var prio string
	err = db.QueryRow("SELECT priority FROM qos_rules WHERE target = 'Misafir Wifi'").Scan(&prio)
	if err != nil || prio != "low" {
		t.Errorf("Yeni kural veritabanında bulunamadı veya yanlış öncelik: %s", prio)
	}

	// 3. Test POST /api/qos/rules (HTMX request)
	req = httptest.NewRequest("POST", "/api/qos/rules", strings.NewReader("target=Muhasebe&priority=normal"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rr = httptest.NewRecorder()

	handleCreateQoSRule(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("HTMX POST /api/qos/rules beklenen HTTP 200, alınan: %d", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "Kaydedildi ✓") {
		t.Errorf("HTMX yanıtı beklenen 'Kaydedildi ✓' geri bildirimini içermiyor: %s", rr.Body.String())
	}
}
