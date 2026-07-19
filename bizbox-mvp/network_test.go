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

func TestNetworkSegmentation(t *testing.T) {
	// Setup in-memory SQLite database
	var err error
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Test veritabanı bağlantı hatası: %v", err)
	}
	defer db.Close()

	// Initialize tables and seed default segments
	InitNetworkDB()
	InitQosDB()

	// Test 1: Seed verification
	segments := ListNetworkSegments()
	if len(segments) != 3 {
		t.Errorf("Beklenen varsayılan segment sayısı 3, alınan %d", len(segments))
	}

	expectedSeed := map[string]int{
		"Muhasebe":     10,
		"Misafir Wifi": 20,
		"Kameralar":    30,
	}

	for _, seg := range segments {
		vlan, ok := expectedSeed[seg.Name]
		if !ok {
			t.Errorf("Beklenmedik seed segment adı: %s", seg.Name)
		} else if seg.VlanID != vlan {
			t.Errorf("%s için beklenen VLAN %d, alınan %d", seg.Name, vlan, seg.VlanID)
		}
	}

	// Test 2: CreateSegment
	err = CreateSegment("Yazilim", 40)
	if err != nil {
		t.Fatalf("Segment oluşturma hatası: %v", err)
	}

	segments = ListNetworkSegments()
	if len(segments) != 4 {
		t.Errorf("Segment oluşturulduktan sonra beklenen segment sayısı 4, alınan %d", len(segments))
	}

	found := false
	for _, seg := range segments {
		if seg.Name == "Yazilim" {
			found = true
			if seg.VlanID != 40 {
				t.Errorf("Yeni segment için beklenen VLAN ID 40, alınan %d", seg.VlanID)
			}
		}
	}
	if !found {
		t.Error("Yeni oluşturulan 'Yazilim' segmenti listede bulunamadı")
	}

	// Test 3: AssignVMToSegment
	err = AssignVMToSegment("vm-test-1", "Muhasebe")
	if err != nil {
		t.Fatalf("VM atama hatası: %v", err)
	}

	segments = ListNetworkSegments()
	var muhasebeSeg *Segment
	for i, seg := range segments {
		if seg.Name == "Muhasebe" {
			muhasebeSeg = &segments[i]
		}
	}

	if muhasebeSeg == nil {
		t.Fatal("'Muhasebe' segmenti bulunamadı")
	}
	if len(muhasebeSeg.VMs) != 1 || muhasebeSeg.VMs[0] != "vm-test-1" {
		t.Errorf("Muhasebe segmentine atanan VM listesi beklenen gibi değil: %v", muhasebeSeg.VMs)
	}
}

func TestNetworkAPI(t *testing.T) {
	// Setup in-memory SQLite database
	var err error
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Test veritabanı bağlantı hatası: %v", err)
	}
	defer db.Close()

	InitNetworkDB()
	InitQosDB()

	// 1. Test GET /api/network/segments returns JSON segment list
	req := httptest.NewRequest("GET", "/api/network/segments", nil)
	rr := httptest.NewRecorder()

	handleGetSegments(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Beklenen HTTP durum kodu %d, alınan %d", http.StatusOK, rr.Code)
	}

	var segments []Segment
	err = json.Unmarshal(rr.Body.Bytes(), &segments)
	if err != nil {
		t.Fatalf("JSON decode hatası: %v", err)
	}

	if len(segments) != 3 {
		t.Errorf("JSON listesinde beklenen segment sayısı 3, alınan %d", len(segments))
	}

	// 2. Test POST /api/network/segments creates new segment with auto-assigned VLAN
	body := `{"name": "Pazarlama"}`
	req = httptest.NewRequest("POST", "/api/network/segments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()

	handleCreateSegmentAPI(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("Beklenen HTTP durum kodu %d, alınan %d", http.StatusCreated, rr.Code)
	}

	// Check if created successfully in DB with VLAN 40 (max 30 + 10)
	var segVlan int
	err = db.QueryRow("SELECT vlan_id FROM network_segments WHERE name = 'Pazarlama'").Scan(&segVlan)
	if err != nil {
		t.Fatalf("Yeni oluşturulan segment veritabanında bulunamadı: %v", err)
	}
	if segVlan != 40 {
		t.Errorf("Beklenen VLAN ID 40, alınan %d", segVlan)
	}
}
