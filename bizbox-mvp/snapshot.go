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

// Snapshot represents ZFS snapshot metadata
type Snapshot struct {
	Name        string    `json:"name"`        // e.g. "rft/virtual-machines/vm1@snap1"
	ShortName   string    `json:"short_name"`  // e.g. "snap1"
	Dataset     string    `json:"dataset"`     // e.g. "rft/virtual-machines/vm1"
	CreatedAt   time.Time `json:"created_at"`
	IsAutomatic bool      `json:"is_automatic"`
}

// AutoSnapshotInterval is the duration between automatic snapshots
var AutoSnapshotInterval = 15 * time.Minute

func init() {
	if val := os.Getenv("AUTO_SNAPSHOT_INTERVAL_MINUTES"); val != "" {
		if mins, err := strconv.Atoi(val); err == nil {
			AutoSnapshotInterval = time.Duration(mins) * time.Minute
		}
	}
}

// getDatasetForInstance returns the ZFS dataset name for the instance
func getDatasetForInstance(name string, instType string) string {
	if instType == "virtual-machine" {
		return fmt.Sprintf("rft/virtual-machines/%s", name)
	}
	return fmt.Sprintf("rft/containers/%s", name)
}

// ListSnapshots executes ZFS command to list snapshots of a dataset
func ListSnapshots(dataset string) []Snapshot {
	if dataset == "" {
		return []Snapshot{}
	}

	// -H: no headers, script-friendly
	// -p: print numbers (like epoch timestamps for creation) exactly
	// -t snapshot: only show snapshots
	// -o name,creation: output name and creation epoch
	// -r: recursive (we can filter for exact match)
	cmd := exec.Command("zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation", "-r", dataset)
	output, err := cmd.Output()
	if err != nil {
		// Log error to stdout and return empty
		fmt.Printf("[ZFS] list error for %s: %v\n", dataset, err)
		return []Snapshot{}
	}

	var snapshots []Snapshot
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			fields = strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
		}

		fullName := fields[0]
		epochStr := fields[1]

		epoch, err := strconv.ParseInt(epochStr, 10, 64)
		if err != nil {
			continue
		}

		parts := strings.SplitN(fullName, "@", 2)
		if len(parts) < 2 {
			continue
		}
		dsName := parts[0]
		shortName := parts[1]

		// Ensure we only show snapshots belonging to this exact dataset (not sub-datasets)
		if dsName != dataset {
			continue
		}

		createdAt := time.Unix(epoch, 0)
		isAutomatic := strings.HasPrefix(shortName, "auto_")

		snapshots = append(snapshots, Snapshot{
			Name:        fullName,
			ShortName:   shortName,
			Dataset:     dsName,
			CreatedAt:   createdAt,
			IsAutomatic: isAutomatic,
		})
	}

	return snapshots
}

// CreateSnapshot creates a manual snapshot on the given dataset
func CreateSnapshot(dataset string) error {
	timestamp := time.Now().Unix()
	snapName := fmt.Sprintf("manual_%d", timestamp)
	fullName := fmt.Sprintf("%s@%s", dataset, snapName)

	cmd := exec.Command("zfs", "snapshot", fullName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ZFS manual snapshot oluşturulamadı: %w", err)
	}
	return nil
}

// CreateAutoSnapshot creates an automatic snapshot on the given dataset
func CreateAutoSnapshot(dataset string) error {
	timestamp := time.Now().Unix()
	snapName := fmt.Sprintf("auto_%d", timestamp)
	fullName := fmt.Sprintf("%s@%s", dataset, snapName)

	cmd := exec.Command("zfs", "snapshot", fullName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ZFS auto snapshot oluşturulamadı: %w", err)
	}
	return nil
}

// RollbackSnapshot rolls back a dataset to a specific snapshot
func RollbackSnapshot(snapshotName string) error {

	// -r destroys any snapshots and bookmarks more recent than the target one
	cmd := exec.Command("zfs", "rollback", "-r", snapshotName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ZFS rollback başarısız (%s): %w", snapshotName, err)
	}
	return nil
}

// DestroySnapshot deletes a ZFS snapshot
func DestroySnapshot(snapshotName string) error {
	cmd := exec.Command("zfs", "destroy", snapshotName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ZFS snapshot silinemedi (%s): %w", snapshotName, err)
	}
	return nil
}

// StartAutoSnapshotScheduler initializes the background scheduler for snapshots and retention
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
		dataset := getDatasetForInstance(inst.Name, inst.Type)

		// 1. Create automatic snapshot
		if err := CreateAutoSnapshot(dataset); err != nil {
			fmt.Printf("[AutoSnapshot] Hata - %s için otomatik snapshot alınamadı: %v\n", inst.Name, err)
		} else {
			fmt.Printf("[AutoSnapshot] Başarılı - %s için otomatik snapshot alındı\n", inst.Name)
		}

		// 2. retention: clean up auto snapshots older than 48 hours
		snaps := ListSnapshots(dataset)
		for _, snap := range snaps {
			if snap.IsAutomatic && now.Sub(snap.CreatedAt) > 48*time.Hour {
				if err := DestroySnapshot(snap.Name); err != nil {
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

	inst, _, err := c.GetInstance(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	dataset := getDatasetForInstance(vmName, inst.Type)
	snaps := ListSnapshots(dataset)

	type SnapshotAPIResponse struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		ShortName   string    `json:"short_name"`
		Dataset     string    `json:"dataset"`
		CreatedAt   time.Time `json:"created_at"`
		IsAutomatic bool      `json:"is_automatic"`
	}

	var response []SnapshotAPIResponse
	for _, s := range snaps {
		response = append(response, SnapshotAPIResponse{
			ID:          fmt.Sprintf("%s@%s", vmName, s.ShortName),
			Name:        s.Name,
			ShortName:   s.ShortName,
			Dataset:     s.Dataset,
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
	var req struct {
		VM string `json:"vm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VM == "" {
		http.Error(w, "Geçersiz istek gövdesi (beklenen: {'vm': 'vmName'})", http.StatusBadRequest)
		return
	}

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	inst, _, err := c.GetInstance(req.VM)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	dataset := getDatasetForInstance(req.VM, inst.Type)
	if err := CreateSnapshot(dataset); err != nil {
		http.Error(w, fmt.Sprintf("Snapshot oluşturulamadı: %v", err), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Yedekleme", req.VM, "Başarılı")
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

	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Confirm {
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

	inst, _, err := c.GetInstance(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	dataset := getDatasetForInstance(vmName, inst.Type)
	fullSnapshotName := fmt.Sprintf("%s@%s", dataset, snapShortName)

	status, err := GetVMStatus(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM durum bilgisi alınamadı: %v", err), http.StatusInternalServerError)
		return
	}

	wasRunning := status.Status == "running"

	// If the VM is running, stop it first
	if wasRunning {
		fmt.Printf("[Rollback] VM %s durduruluyor...\n", vmName)
		if err := StopVM(vmName); err != nil {
			http.Error(w, fmt.Sprintf("VM durdurulamadı: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Rollback
	fmt.Printf("[Rollback] ZFS geri yükleme yapılıyor: %s...\n", fullSnapshotName)
	if err := RollbackSnapshot(fullSnapshotName); err != nil {
		// Restart VM if it was running before failure
		if wasRunning {
			_ = StartVM(vmName)
		}
		http.Error(w, fmt.Sprintf("ZFS geri yükleme başarısız: %v", err), http.StatusInternalServerError)
		return
	}

	// Start VM again if it was running
	if wasRunning {
		fmt.Printf("[Rollback] VM %s yeniden başlatılıyor...\n", vmName)
		if err := StartVM(vmName); err != nil {
			http.Error(w, fmt.Sprintf("VM geri yükleme sonrası başlatılamadı: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	message := "Geri dönüldü."
	if wasRunning {
		message = "Geri dönüldü, VM yeniden başlatıldı."
	} else {
		message = "Geri dönüldü, VM kapalı kalmaya devam ediyor."
	}

	LogSystemEvent(getUsername(r), "Geri Yükleme", vmName, "Başarılı")
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

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	inst, _, err := c.GetInstance(name)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	dataset := getDatasetForInstance(name, inst.Type)
	snaps := ListSnapshots(dataset)

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
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = templates.ExecuteTemplate(w, "vm-detail.html", data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Şablon oluşturulamadı: %v", err), http.StatusInternalServerError)
	}
}

// ListAllSnapshots executes ZFS command to list all snapshots in the system
func ListAllSnapshots() []Snapshot {
	cmd := exec.Command("zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation")
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("[ZFS] list all error: %v\n", err)
		return []Snapshot{}
	}

	var snapshots []Snapshot
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			fields = strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
		}

		fullName := fields[0]
		epochStr := fields[1]

		epoch, err := strconv.ParseInt(epochStr, 10, 64)
		if err != nil {
			continue
		}

		parts := strings.SplitN(fullName, "@", 2)
		if len(parts) < 2 {
			continue
		}
		dsName := parts[0]
		shortName := parts[1]

		createdAt := time.Unix(epoch, 0)
		isAutomatic := strings.HasPrefix(shortName, "auto_")

		snapshots = append(snapshots, Snapshot{
			Name:        fullName,
			ShortName:   shortName,
			Dataset:     dsName,
			CreatedAt:   createdAt,
			IsAutomatic: isAutomatic,
		})
	}

	return snapshots
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

	if err := DestroySnapshot(snapName); err != nil {
		LogSystemEvent(getUsername(r), "Yedek Silme", snapName, "Başarısız")
		http.Error(w, fmt.Sprintf("Yedek silinemedi: %v", err), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Yedek Silme", snapName, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Yedek başarıyla silindi."})
}
}
