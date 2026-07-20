package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	incus "github.com/lxc/incus/client"
	"github.com/lxc/incus/shared/api"
)

// Snapshot represents an Incus snapshot metadata
type Snapshot struct {
	Name        string    `json:"name"`
	ShortName   string    `json:"short_name"`
	VMName      string    `json:"vm_name"`
	CreatedAt   time.Time `json:"created_at"`
	IsAutomatic bool      `json:"is_automatic"`
}

// AutoSnapshotInterval is the duration between automatic snapshots
var AutoSnapshotInterval = 15 * time.Minute

func init() {
	if val := os.Getenv("AUTO_SNAPSHOT_INTERVAL_MINUTES"); val != "" {
		if mins, err := strconv.Atoi(val); err == nil && mins > 0 {
			AutoSnapshotInterval = time.Duration(mins) * time.Minute
		}
	}
}

// ListInstanceSnapshots executes Incus command to list snapshots of an instance
func ListInstanceSnapshots(vmName string) []Snapshot {
	if vmName == "" {
		return []Snapshot{}
	}

	cmd := exec.Command("incus", "snapshot", "list", vmName, "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("[Incus] list snapshot error for %s: %v\n", vmName, err)
		return []Snapshot{}
	}

	var incusSnaps []struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}

	if err := json.Unmarshal(output, &incusSnaps); err != nil {
		fmt.Printf("[Incus] unmarshal snapshot error for %s: %v\n", vmName, err)
		return []Snapshot{}
	}

	var snapshots []Snapshot
	for _, s := range incusSnaps {
		isAutomatic := strings.HasPrefix(s.Name, "auto_")
		snapshots = append(snapshots, Snapshot{
			Name:        fmt.Sprintf("%s@%s", vmName, s.Name),
			ShortName:   s.Name,
			VMName:      vmName,
			CreatedAt:   s.CreatedAt,
			IsAutomatic: isAutomatic,
		})
	}

	return snapshots
}

// CreateInstanceSnapshot creates a manual snapshot on the given instance
func CreateInstanceSnapshot(vmName string) error {
	timestamp := time.Now().Unix()
	snapName := fmt.Sprintf("manual_%d", timestamp)

	cmd := exec.Command("incus", "snapshot", "create", vmName, snapName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Incus manual snapshot oluşturulamadı: %w. Detay: %s", err, string(out))
	}
	return nil
}

// CreateAutoInstanceSnapshot creates an automatic snapshot on the given instance
func CreateAutoInstanceSnapshot(vmName string) error {
	timestamp := time.Now().Unix()
	snapName := fmt.Sprintf("auto_%d", timestamp)

	cmd := exec.Command("incus", "snapshot", "create", vmName, snapName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Incus auto snapshot oluşturulamadı: %w. Detay: %s", err, string(out))
	}
	return nil
}

// RollbackInstanceSnapshot rolls back a dataset to a specific snapshot
func RollbackInstanceSnapshot(vmName string, snapshotName string) error {
	cmd := exec.Command("incus", "snapshot", "restore", vmName, snapshotName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Incus rollback başarısız (%s): %w. Detay: %s", snapshotName, err, string(out))
	}
	return nil
}

// DestroyInstanceSnapshot deletes a snapshot
func DestroyInstanceSnapshot(vmName string, snapshotName string) error {
	cmd := exec.Command("incus", "snapshot", "delete", vmName, snapshotName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Incus snapshot silinemedi (%s): %w. Detay: %s", snapshotName, err, string(out))
	}
	return nil
}

// StartAutoSnapshotScheduler initializes the background scheduler for snapshots and retentiontention
func StartAutoSnapshotScheduler() {
	fmt.Printf("[AutoSnapshot] Zamanlayıcı başlatıldı. Çalışma aralığı: %v\n", AutoSnapshotInterval)
	ticker := time.NewTicker(AutoSnapshotInterval)
	go func() {
		for range ticker.C {
			takeAutoSnapshotsAndClean()
		}
	}()
}

// takeAutoSnapshotsAndClean runs the auto snapshot creation and old snapshot deletion
func takeAutoSnapshotsAndClean() {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		fmt.Printf("[AutoSnapshot] Incus bağlantı hatası: %v\n", err)
		return
	}

	instances, err := c.GetInstances(api.InstanceTypeAny)
	if err != nil {
		fmt.Printf("[AutoSnapshot] Örnek listesi alınamadı: %v\n", err)
		return
	}

	now := time.Now()
	for _, inst := range instances {

		// 1. Create automatic snapshot
		if err := CreateAutoInstanceSnapshot(inst.Name); err != nil {
			fmt.Printf("[AutoSnapshot] Hata - %s için otomatik snapshot alınamadı: %v\n", inst.Name, err)
		} else {
			fmt.Printf("[AutoSnapshot] Başarılı - %s için otomatik snapshot alındı\n", inst.Name)
		}

	// 2. retention: clean up auto snapshots older than 48 hours
		snaps := ListInstanceSnapshots(inst.Name)
		for _, snap := range snaps {
			if snap.IsAutomatic && now.Sub(snap.CreatedAt) > 48*time.Hour {
				if err := DestroyInstanceSnapshot(snap.VMName, snap.Name); err != nil {
					fmt.Printf("[AutoSnapshot] Hata - Eski snapshot silinemedi %s: %v\n", snap.Name, err)
				} else {
					fmt.Printf("[AutoSnapshot] Başarılı - 48 saatten eski snapshot silindi: %s\n", snap.Name)
				}
			}
		}
	}
}

// API: GET /api/snapshots?vm={name}
func handleGetSnapshots(w http.ResponseWriter, r *http.Request) {
	vmName := r.URL.Query().Get("vm")
	if vmName == "" {
		http.Error(w, "vm parametresi eksik", http.StatusBadRequest)
		return
	}

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	_, _, err = c.GetInstance(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	snaps := ListInstanceSnapshots(vmName)

	type SnapshotAPIResponse struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		ShortName   string    `json:"short_name"`
		VMName      string    `json:"vm_name"`
		CreatedAt   time.Time `json:"created_at"`
		IsAutomatic bool      `json:"is_automatic"`
	}

	var response []SnapshotAPIResponse
	for _, s := range snaps {
		response = append(response, SnapshotAPIResponse{
			ID:          fmt.Sprintf("%s@%s", vmName, s.ShortName),
			Name:        s.Name,
			ShortName:   s.ShortName,
			VMName:      s.VMName,
			CreatedAt:   s.CreatedAt,
			IsAutomatic: s.IsAutomatic,
		})
	}
	if response == nil {
		response = []SnapshotAPIResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// API: POST /api/snapshots (Body: {"vm": "vmName"})
func handleCreateSnapshotAPI(w http.ResponseWriter, r *http.Request) {
	vm := r.FormValue("vm")
	if vm == "" {
		// Fallback to JSON if not form encoded
		var req struct {
			VM string `json:"vm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.VM != "" {
			vm = req.VM
		}
	}

	if vm == "" {
		http.Error(w, "Geçersiz istek gövdesi (vm parametresi eksik)", http.StatusBadRequest)
		return
	}

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	_, _, err = c.GetInstance(vm)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	startTime := time.Now()
	
	if err := CreateInstanceSnapshot(vm); err != nil {
		duration := time.Since(startTime)
		LogSystemEvent(getUsername(r), "Yedekleme", vm, fmt.Sprintf("Başarısız (%.1f sn)", duration.Seconds()))
		http.Error(w, fmt.Sprintf("Snapshot oluşturulamadı: %v", err), http.StatusInternalServerError)
		return
	}

	duration := time.Since(startTime)
	LogSystemEvent(getUsername(r), "Yedekleme", vm, fmt.Sprintf("Başarılı (%.1f sn)", duration.Seconds()))
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Snapshot başarıyla oluşturuldu.",
	})
}

// API: POST /api/snapshots/{id}/rollback (Body: {"confirm": true})
func handleRollbackSnapshotAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Snapshot ID parametresi eksik", http.StatusBadRequest)
		return
	}

	confirmVal := r.FormValue("confirm")
	confirm := (confirmVal == "true" || confirmVal == "1")

	if !confirm {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Confirm {
			confirm = true
		}
	}

	if !confirm {
		http.Error(w, "Geri dönmek için onay vermelisiniz ('confirm': true)", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(id, "@", 2)
	if len(parts) < 2 {
		http.Error(w, "Geçersiz Snapshot ID formatı", http.StatusBadRequest)
		return
	}
	vmName := parts[0]
	snapShortName := parts[1]

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	_, _, err = c.GetInstance(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	status, err := GetVMStatus(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM durum bilgisi alınamadı: %v", err), http.StatusInternalServerError)
		return
	}

	startTime := time.Now()

	wasRunning := status.Status == "running"

	// If the VM is running, stop it first
	if wasRunning {
		fmt.Printf("[Rollback] VM %s durduruluyor...\n", vmName)
		if err := StopVM(vmName); err != nil {
			duration := time.Since(startTime)
			LogSystemEvent(getUsername(r), "Geri Yükleme", vmName, fmt.Sprintf("Başarısız (VM Durdurulamadı) (%.1f sn)", duration.Seconds()))
			http.Error(w, fmt.Sprintf("VM durdurulamadı: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Rollback
	fmt.Printf("[Rollback] Geri yükleme yapılıyor: %s...\n", snapShortName)
	if err := RollbackInstanceSnapshot(vmName, snapShortName); err != nil {
		// Restart VM if it was running before failure
		if wasRunning {
			_ = StartVM(vmName)
		}
		duration := time.Since(startTime)
		LogSystemEvent(getUsername(r), "Geri Yükleme", vmName, fmt.Sprintf("Başarısız (%.1f sn)", duration.Seconds()))
		http.Error(w, fmt.Sprintf("Geri yükleme başarısız: %v", err), http.StatusInternalServerError)
		return
	}

	// Start VM again if it was running
	if wasRunning {
		fmt.Printf("[Rollback] VM %s yeniden başlatılıyor...\n", vmName)
		if err := StartVM(vmName); err != nil {
			duration := time.Since(startTime)
			LogSystemEvent(getUsername(r), "Geri Yükleme", vmName, fmt.Sprintf("Başarısız (Başlatılamadı) (%.1f sn)", duration.Seconds()))
			http.Error(w, fmt.Sprintf("VM geri yükleme sonrası başlatılamadı: %v", err), http.StatusInternalServerError)
			return
		}
	}

	duration := time.Since(startTime)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	message := "Geri dönüldü."
	if wasRunning {
		message = "Geri dönüldü, VM yeniden başlatıldı."
	} else {
		message = "Geri dönüldü, VM kapalı kalmaya devam ediyor."
	}

	LogSystemEvent(getUsername(r), "Geri Yükleme", vmName, fmt.Sprintf("Başarılı (%.1f sn)", duration.Seconds()))
	json.NewEncoder(w).Encode(map[string]string{
		"message": message,
	})
}

// GET /api/vms/{name}/detail - Returns HTML detail fragment
func handleGetVMDetailHTML(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "İsim parametresi eksik", http.StatusBadRequest)
		return
	}

	status, err := GetVMStatus(name)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM detayları alınamadı: %v", err), http.StatusInternalServerError)
		return
	}

	snaps := ListInstanceSnapshots(name)

	type SnapshotViewItem struct {
		ID            string    `json:"id"`
		ShortName     string    `json:"short_name"`
		FormattedDate string    `json:"formatted_date"`
		IsAutomatic   bool      `json:"is_automatic"`
		RawTime       time.Time `json:"raw_time"`
	}

	var snapItems []SnapshotViewItem
	for _, s := range snaps {
		snapItems = append(snapItems, SnapshotViewItem{
			ID:            fmt.Sprintf("%s@%s", name, s.ShortName),
			ShortName:     s.ShortName,
			FormattedDate: s.CreatedAt.Format("2006-01-02 15:04:05"),
			IsAutomatic:   s.IsAutomatic,
			RawTime:       s.CreatedAt,
		})
	}

	cpuVal := 1
	fmt.Sscanf(status.CPULimit, "%d", &cpuVal)
	if cpuVal <= 0 {
		cpuVal = 1
	}

	ramVal := 1
	fmt.Sscanf(status.RAMLimit, "%d", &ramVal)
	if ramVal <= 0 {
		ramVal = 1
	}

	// Fetch backup logs for this VM
	type VMLog struct {
		SystemLog
		IsSuccess bool
	}
	var vmLogs []VMLog
	rows, err := db.Query("SELECT id, datetime(timestamp, 'localtime'), user, action, target, status FROM system_logs WHERE target = ? AND action IN ('Yedekleme', 'Geri Yükleme', 'Yedek Silme') ORDER BY id DESC LIMIT 10", name)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var l SystemLog
			if err := rows.Scan(&l.ID, &l.Timestamp, &l.User, &l.Action, &l.Target, &l.Status); err == nil {
				vmLogs = append(vmLogs, VMLog{
					SystemLog: l,
					IsSuccess: strings.Contains(l.Status, "Başarılı"),
				})
			}
		}
	}

	data := struct {
		Name      string
		Status    string
		CPULimit  string
		RAMLimit  string
		CPUVal    int
		RAMVal    int
		IPAddress string
		CreatedAt string
		Snapshots []SnapshotViewItem
		Logs      []VMLog
	}{
		Name:      status.Name,
		Status:    status.Status,
		CPULimit:  status.CPULimit,
		RAMLimit:  status.RAMLimit,
		CPUVal:    cpuVal,
		RAMVal:    ramVal,
		IPAddress: status.IPAddress,
		CreatedAt: status.CreatedAt,
		Snapshots: snapItems,
		Logs:      vmLogs,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = templates.ExecuteTemplate(w, "vm-detail.html", data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Şablon oluşturulamadı: %v", err), http.StatusInternalServerError)
	}
}

// ListAllSnapshots gathers snapshots from all Incus instances
func ListAllSnapshots() []Snapshot {
	vms, err := getAllVMNames()
	if err != nil {
		fmt.Printf("[Incus] ListAllSnapshots error getting VMs: %v\n", err)
		return []Snapshot{}
	}

	var allSnapshots []Snapshot
	for _, vm := range vms {
		snaps := ListInstanceSnapshots(vm)
		allSnapshots = append(allSnapshots, snaps...)
	}

	return allSnapshots
}

// GET /api/backup/page
func handleGetBackupPage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "true" {
		http.Error(w, "Yalnızca HTMX istekleri kabul edilir", http.StatusBadRequest)
		return
	}

	snaps := ListAllSnapshots()
	vms, _ := getAllVMNames()

	data := struct {
		Snapshots []Snapshot
		VMs       []string
	}{
		Snapshots: snaps,
		VMs:       vms,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	err := templates.ExecuteTemplate(w, "backup.html", data)
	if err != nil {
		log.Printf("[Backup] backup.html render hatası: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// POST /api/snapshots/destroy (Body: name=fullName)
func handleDestroySnapshotAPI(w http.ResponseWriter, r *http.Request) {
	snapName := r.FormValue("name")
	if snapName == "" {
		http.Error(w, "Yedek ismi eksik", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(snapName, "@", 2)
	if len(parts) < 2 {
		http.Error(w, "Geçersiz Yedek formatı", http.StatusBadRequest)
		return
	}
	vmName := parts[0]
	shortName := parts[1]

	if err := DestroyInstanceSnapshot(vmName, shortName); err != nil {
		LogSystemEvent(getUsername(r), "Yedek Silme", snapName, "Başarısız")
		http.Error(w, fmt.Sprintf("Yedek silinemedi: %v", err), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Yedek Silme", snapName, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Yedek başarıyla silindi."})
}
