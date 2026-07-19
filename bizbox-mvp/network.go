package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"

	incus "github.com/lxc/incus/client"
	"github.com/lxc/incus/shared/api"
)

// Segment represents a network segment metadata and its assigned VMs
type Segment struct {
	Name   string   `json:"name"`
	VlanID int      `json:"vlan_id"`
	VMs    []string `json:"vms"`
}

// InitNetworkDB initializes the SQLite tables required for network segmentation and seeds default values.
func InitNetworkDB() {
	querySegments := `
	CREATE TABLE IF NOT EXISTS network_segments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE,
		vlan_id INTEGER UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err := db.Exec(querySegments)
	if err != nil {
		log.Fatalf("network_segments tablosu oluşturulurken hata: %v", err)
	}

	queryVMs := `
	CREATE TABLE IF NOT EXISTS network_segment_vms (
		vm_name TEXT PRIMARY KEY,
		segment_name TEXT,
		FOREIGN KEY(segment_name) REFERENCES network_segments(name) ON DELETE CASCADE
	);`
	_, err = db.Exec(queryVMs)
	if err != nil {
		log.Fatalf("network_segment_vms tablosu oluşturulurken hata: %v", err)
	}

	// No seed database segments in production to prevent mock/placeholder data
}

// syncNetworkDB checks actual VMs in Incus and removes ghost VMs from the network DB
func syncNetworkDB() {
	liveVMs, err := getAllVMNames()
	if err != nil {
		log.Printf("[Network] VM isimleri senkronize edilemedi: %v", err)
		return
	}

	liveMap := make(map[string]bool)
	for _, vm := range liveVMs {
		liveMap[vm] = true
	}

	rows, err := db.Query("SELECT vm_name FROM network_segment_vms")
	if err != nil {
		return
	}
	defer rows.Close()

	var toDelete []string
	for rows.Next() {
		var vm string
		if err := rows.Scan(&vm); err == nil {
			if !liveMap[vm] {
				toDelete = append(toDelete, vm)
			}
		}
	}
	rows.Close()

	for _, vm := range toDelete {
		log.Printf("[Network] Ölü VM veritabanından temizleniyor: %s", vm)
		db.Exec("DELETE FROM network_segment_vms WHERE vm_name = ?", vm)
	}
}

// ListNetworkSegments lists all segments and their assigned VMs from database
func ListNetworkSegments() []Segment {
	syncNetworkDB()

	rows, err := db.Query("SELECT name, vlan_id FROM network_segments ORDER BY vlan_id ASC")
	if err != nil {
		log.Printf("Segmentler listelenirken hata: %v", err)
		return []Segment{}
	}
	defer rows.Close()

	var segments []Segment
	for rows.Next() {
		var seg Segment
		if err := rows.Scan(&seg.Name, &seg.VlanID); err != nil {
			continue
		}
		seg.VMs = []string{}
		segments = append(segments, seg)
	}

	// Retrieve VM assignments
	for i, seg := range segments {
		vmRows, err := db.Query("SELECT vm_name FROM network_segment_vms WHERE segment_name = ?", seg.Name)
		if err != nil {
			continue
		}
		for vmRows.Next() {
			var vmName string
			if err := vmRows.Scan(&vmName); err == nil {
				segments[i].VMs = append(segments[i].VMs, vmName)
			}
		}
		vmRows.Close()
	}

	return segments
}

// CreateSegment registers a new segment and runs OVS wrapper setup commands
func CreateSegment(name string, vlanID int) error {
	_, err := db.Exec("INSERT INTO network_segments (name, vlan_id) VALUES (?, ?)", name, vlanID)
	if err != nil {
		return fmt.Errorf("veritabanına segment eklenirken hata: %w", err)
	}

	return createOVSSegment(name, vlanID)
}

// AssignVMToSegment links a VM to a segment in the database and applies VLAN tagging on the OVS port
func AssignVMToSegment(vmName string, segmentName string) error {
	var vlanID int
	err := db.QueryRow("SELECT vlan_id FROM network_segments WHERE name = ?", segmentName).Scan(&vlanID)
	if err != nil {
		return fmt.Errorf("hedef segment bulunamadı: %w", err)
	}

	_, err = db.Exec(`
		INSERT INTO network_segment_vms (vm_name, segment_name)
		VALUES (?, ?)
		ON CONFLICT(vm_name) DO UPDATE SET segment_name = excluded.segment_name
	`, vmName, segmentName)
	if err != nil {
		return fmt.Errorf("veritabanı atama hatası: %w", err)
	}

	// Apply inherited QoS settings of the segment to this VM (or VM's direct rule if any)
	_ = ApplyQoSForVM(vmName)

	// OVS VLAN Tagging Command wrapper:
	// A VM's interface name attached to OVS on the host typically follows "veth-<vmName>".
	//
	// OVS Komutu:
	// ovs-vsctl set port veth-<vmName> tag=<vlanID>
	//
	// Geliştirme ve simülasyon ortamları için komut başarısız olsa bile log yazarak devam ediyoruz.
	portName := fmt.Sprintf("veth-%s", vmName)
	cmd := exec.Command("ovs-vsctl", "set", "port", portName, fmt.Sprintf("tag=%d", vlanID))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Port tagging komutu çalıştırılamadı: '%s' (VLAN: %d) - Hata: %w. Detay: %s", portName, vlanID, err, string(out))
	}
	log.Printf("[OVS] '%s' portu başarıyla VLAN %d (Segment: %s) ile etiketlendi.", portName, vlanID, segmentName)
	return nil
}

// DeleteSegment deletes a segment, un-tags its VMs, and removes OVS flow rules
func DeleteSegment(name string) error {
	syncNetworkDB()

	var vlanID int
	err := db.QueryRow("SELECT vlan_id FROM network_segments WHERE name = ?", name).Scan(&vlanID)
	if err != nil {
		return fmt.Errorf("segment bulunamadı: %w", err)
	}

	// Retrieve VMs assigned to this segment to see if it is in use
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM network_segment_vms WHERE segment_name = ?", name).Scan(&count)
	if err == nil && count > 0 {
		return fmt.Errorf("bu ağ segmentine atanmış %d adet sanal makine bulunuyor. Lütfen önce onları başka bir ağa taşıyın", count)
	}

	// Delete from database
	_, err = db.Exec("DELETE FROM network_segments WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("segment silinirken veritabanı hatası: %w", err)
	}

	// Remove OVS flow rules
	_ = exec.Command("ovs-ofctl", "del-flows", "br-int", fmt.Sprintf("dl_vlan=%d", vlanID)).Run()

	// Also remove QoS rule if any
	_, _ = db.Exec("DELETE FROM qos_rules WHERE target = ?", name)

	return nil
}

// createOVSSegment wraps OVS command line actions and applies default security flow rules
func createOVSSegment(name string, vlanID int) error {
	log.Printf("[OVS] Segment oluşturuluyor: %s (VLAN ID: %d)", name, vlanID)

	// 1. Ensure Integration Bridge (br-int) exists
	cmdBridge := exec.Command("ovs-vsctl", "add-br", "br-int")
	if out, err := cmdBridge.CombinedOutput(); err != nil {
		// If bridge already exists, that's not a fatal error
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("br-int köprüsü oluşturulamadı: %w. Detay: %s", err, string(out))
		}
	} else {
		log.Printf("[OVS] br-int köprüsü oluşturuldu/doğrulandı.")
	}

	// 2. Apply Security Flow Rules (Default Deny between VLANs)
	
	// VLAN yönlendirme kuralı
	cmdFlow := exec.Command("ovs-ofctl", "add-flow", "br-int", fmt.Sprintf("priority=1000,dl_vlan=%d,actions=resubmit(,1)", vlanID))
	if out, err := cmdFlow.CombinedOutput(); err != nil {
		return fmt.Errorf("ovs-ofctl add-flow resubmit başarısız: %w. Detay: %s", err, string(out))
	}

	// İzolasyon tablosu varsayılan DROP kuralı
	cmdDropFlow := exec.Command("ovs-ofctl", "add-flow", "br-int", "table=1,priority=1,actions=drop")
	if out, err := cmdDropFlow.CombinedOutput(); err != nil {
		return fmt.Errorf("ovs-ofctl add-flow table=1 drop başarısız: %w. Detay: %s", err, string(out))
	}

	// Aynı VLAN içi haberleşme kuralı
	cmdAllowFlow := exec.Command("ovs-ofctl", "add-flow", "br-int", fmt.Sprintf("table=1,priority=100,dl_vlan=%d,actions=normal", vlanID))
	if out, err := cmdAllowFlow.CombinedOutput(); err != nil {
		return fmt.Errorf("ovs-ofctl add-flow normal başarısız: %w. Detay: %s", err, string(out))
	}

	return nil
}

// getAllVMNames returns all VM/container instance names from local Incus daemon
func getAllVMNames() ([]string, error) {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return nil, fmt.Errorf("Incus bağlantı hatası: %w", err)
	}
	instances, err := c.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("örnekler alınamadı: %w", err)
	}
	var names []string
	for _, inst := range instances {
		names = append(names, inst.Name)
	}
	return names, nil
}

// GET /api/network/segments
func handleGetSegments(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		segments := ListNetworkSegments()
		allVMs, err := getAllVMNames()
		if err != nil {
			log.Printf("[Network] Incus VM listesi alınamadı: %v", err)
			allVMs = []string{}
		}

		qosRules := GetQoSRulesMap()
		vmToSegment := make(map[string]string)
		for _, seg := range segments {
			for _, vm := range seg.VMs {
				vmToSegment[vm] = seg.Name
			}
		}

		data := struct {
			Segments    []Segment
			AllVMs      []string
			QosRules    map[string]string
			VmToSegment map[string]string
		}{
			Segments:    segments,
			AllVMs:      allVMs,
			QosRules:    qosRules,
			VmToSegment: vmToSegment,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		err = templates.ExecuteTemplate(w, "network.html", data)
		if err != nil {
			log.Printf("[Network] network.html render hatası: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// JSON response
	segments := ListNetworkSegments()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(segments)
}

// POST /api/network/segments
func handleCreateSegmentAPI(w http.ResponseWriter, r *http.Request) {
	var name string

	if r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		name = r.FormValue("name")
	} else {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Geçersiz JSON içeriği", http.StatusBadRequest)
			return
		}
		name = req.Name
	}

	name = strings.TrimSpace(name)
	if name == "" {
		http.Error(w, "Segment ismi boş olamaz", http.StatusBadRequest)
		return
	}

	// Determine VLAN ID
	var vlanID int
	var vlanStr string

	if r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		vlanStr = r.FormValue("vlan_id")
	} else {
		// Assuming JSON logic isn't strictly needing explicit vlan right now, or we parse it
		// But let's just support form for now
	}

	if vlanStr != "" {
		vStrTrim := strings.TrimSpace(vlanStr)
		if vStrTrim != "" {
			importStrConv := true
			_ = importStrConv
			var v int
			fmt.Sscanf(vStrTrim, "%d", &v)
			if v < 1 || v > 4094 {
				http.Error(w, "Geçersiz VLAN ID. Lütfen 1-4094 arası bir değer girin.", http.StatusBadRequest)
				return
			}
			
			// Check if exists
			var count int
			db.QueryRow("SELECT COUNT(*) FROM network_segments WHERE vlan_id = ?", v).Scan(&count)
			if count > 0 {
				http.Error(w, "Bu VLAN ID numarası başka bir ağ segmenti tarafından kullanılıyor.", http.StatusBadRequest)
				return
			}
			vlanID = v
		}
	}

	if vlanID == 0 {
		// Auto-assign VLAN ID (max existing vlan_id + 10, default to 10 if none exist)
		var maxVlan int
		err := db.QueryRow("SELECT COALESCE(MAX(vlan_id), 0) FROM network_segments").Scan(&maxVlan)
		if err != nil {
			maxVlan = 0
		}
		vlanID = maxVlan + 10
		if vlanID < 10 {
			vlanID = 10
		}
		if vlanID > 4094 {
			http.Error(w, "Maksimum VLAN ID sınırına (4094) ulaşıldı", http.StatusBadRequest)
			return
		}
	}

	err := CreateSegment(name, vlanID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Segment oluşturulamadı: %v", err), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "segments-updated")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<div class="alert alert-success" style="padding: 10px; background-color: rgba(22, 163, 74, 0.1); color: var(--success-color); border-radius: var(--radius-btn); margin-bottom: 15px; font-size:13px;">Segment başarıyla oluşturuldu.</div>`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"name":    name,
		"vlan_id": vlanID,
	})
}

// POST /api/network/segments/{name}/assign
func handleAssignVMAPI(w http.ResponseWriter, r *http.Request) {
	segmentName := r.PathValue("name")
	if segmentName == "" {
		http.Error(w, "Segment ismi eksik", http.StatusBadRequest)
		return
	}

	var vmName string
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var req struct {
			VM string `json:"vm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Geçersiz JSON içeriği", http.StatusBadRequest)
			return
		}
		vmName = req.VM
	} else {
		vmName = r.FormValue("vm")
	}

	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		http.Error(w, "Sanal makine ismi belirtilmedi", http.StatusBadRequest)
		return
	}

	err := AssignVMToSegment(vmName, segmentName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Atama işlemi başarısız: %v", err), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "segments-updated")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Başarıyla atandı"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"vm":      vmName,
		"segment": segmentName,
	})
}

// DELETE /api/network/segments/{name}
func handleDeleteSegmentAPI(w http.ResponseWriter, r *http.Request) {
	segmentName := r.PathValue("name")
	if segmentName == "" {
		http.Error(w, "Segment ismi eksik", http.StatusBadRequest)
		return
	}

	err := DeleteSegment(segmentName)
	if err != nil {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Reswap", "beforeend")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(fmt.Sprintf(`<script>alert("Silinemedi: %v");</script>`, err)))
			return
		}
		http.Error(w, fmt.Sprintf("Segment silinemedi: %v", err), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "segments-updated")
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
