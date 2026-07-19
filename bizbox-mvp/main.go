package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
	incus "github.com/lxc/incus/client"
	"github.com/lxc/incus/shared/api"
)

//go:embed templates/* static/*
var files embed.FS

var templates = template.Must(template.ParseFS(files, "templates/*.html"))

type InstanceInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

type SystemLog struct {
	ID        int
	Timestamp string
	User      string
	Action    string
	Target    string
	Status    string
}

func getHostMemoryUsage() (percent int, formatted string, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, "N/A", err
	}
	var total, available int64
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				fmt.Sscanf(fields[1], "%d", &total)
			}
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				fmt.Sscanf(fields[1], "%d", &available)
			}
		}
	}
	if total == 0 {
		return 0, "N/A", fmt.Errorf("could not read total memory")
	}
	used := total - available
	percent = int((float64(used) / float64(total)) * 100)

	totalGB := float64(total) / 1024 / 1024
	usedGB := float64(used) / 1024 / 1024
	formatted = fmt.Sprintf("%.1f GB / %.1f GB", usedGB, totalGB)
	return percent, formatted, nil
}

func getHostDiskUsage() (percent int, formatted string, err error) {
	var stat syscall.Statfs_t
	err = syscall.Statfs("/", &stat)
	if err != nil {
		return 0, "N/A", err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	used := total - free
	percent = int((float64(used) / float64(total)) * 100)

	totalGB := float64(total) / 1024 / 1024 / 1024
	usedGB := float64(used) / 1024 / 1024 / 1024
	formatted = fmt.Sprintf("%.1f GB / %.1f GB", usedGB, totalGB)
	return percent, formatted, nil
}

// CreateVM creates a virtual machine (VM, not a container) in Incus.
func CreateVM(name string, image string, cpu int, memoryGiB int) error {
	socketPath := "/var/lib/incus/unix.socket"

	// Connect to the local Incus daemon using the Unix socket
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus Unix socket bağlantı hatası: %w", err)
	}

	// Prepare image source
	source := api.InstanceSource{
		Type: "image",
	}

	// Parse remote image source if specified (e.g. "images:ubuntu/22.04")
	if strings.Contains(image, ":") {
		parts := strings.SplitN(image, ":", 2)
		remote := parts[0]
		alias := parts[1]
		if remote == "images" {
			source.Server = "https://images.linuxcontainers.org"
			source.Protocol = "simplestreams"
			source.Alias = alias
		} else {
			source.Alias = image
		}
	} else {
		source.Alias = image
	}

	req := api.InstancesPost{
		Name:   name,
		Type:   api.InstanceTypeVM, // "virtual-machine"
		Source: source,
		InstancePut: api.InstancePut{
			Profiles: []string{"default"},
			Config: map[string]string{
				"limits.cpu":    fmt.Sprintf("%d", cpu),
				"limits.memory": fmt.Sprintf("%dGiB", memoryGiB),
			},
		},
	}

	// Request instance creation
	op, err := c.CreateInstance(req)
	if err != nil {
		return fmt.Errorf("VM oluşturma isteği başarısız: %w", err)
	}

	// Wait for creation to complete (this downloads the image and builds the VM)
	err = op.Wait()
	if err != nil {
		return fmt.Errorf("VM oluşturma işlemi tamamlanırken hata oluştu: %w", err)
	}

	return nil
}

// StopVM stops a virtual machine gracefully.
func StopVM(name string) error {
	socketPath := "/var/lib/incus/unix.socket"

	// Connect to local Incus daemon
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus Unix socket bağlantı hatası: %w", err)
	}

	// Verify instance existence
	_, _, err = c.GetInstance(name)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return fmt.Errorf("%s adında bir makine bulunamadı", name)
		}
		return fmt.Errorf("makine bilgisi alınamadı: %w", err)
	}

	reqState := api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
		Force:   false,
	}

	op, err := c.UpdateInstanceState(name, reqState, "")
	if err != nil {
		return fmt.Errorf("VM durdurma isteği başarısız: %w", err)
	}

	err = op.Wait()
	if err != nil {
		return fmt.Errorf("VM durdurulurken hata oluştu: %w", err)
	}

	return nil
}

// StartVM starts a virtual machine.
func StartVM(name string) error {
	socketPath := "/var/lib/incus/unix.socket"

	// Connect to local Incus daemon
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus Unix socket bağlantı hatası: %w", err)
	}

	// Verify instance existence
	_, _, err = c.GetInstance(name)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return fmt.Errorf("%s adında bir makine bulunamadı", name)
		}
		return fmt.Errorf("makine bilgisi alınamadı: %w", err)
	}

	reqState := api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err := c.UpdateInstanceState(name, reqState, "")
	if err != nil {
		return fmt.Errorf("VM başlatma isteği başarısız: %w", err)
	}

	err = op.Wait()
	if err != nil {
		return fmt.Errorf("VM başlatılırken hata oluştu: %w", err)
	}

	return nil
}

// DeleteVM deletes a virtual machine (or container) from Incus.
func DeleteVM(name string) error {
	socketPath := "/var/lib/incus/unix.socket"

	// Connect to local Incus daemon
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus Unix socket bağlantı hatası: %w", err)
	}

	// Verify instance existence
	_, _, err = c.GetInstance(name)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return fmt.Errorf("%s adında bir makine bulunamadı", name)
		}
		return fmt.Errorf("makine bilgisi alınamadı: %w", err)
	}

	// Delete instance
	op, err := c.DeleteInstance(name)
	if err != nil {
		return fmt.Errorf("VM silme isteği başarısız: %w", err)
	}

	err = op.Wait()
	if err != nil {
		return fmt.Errorf("VM silinirken hata oluştu: %w", err)
	}

	return nil
}

type VMStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CPULimit  string `json:"cpu_limit"`
	RAMLimit  string `json:"ram_limit"`
	IPAddress string `json:"ip_address"`
	CreatedAt string `json:"created_at"`
}

// GetVMStatus retrieves the detailed status of a virtual machine.
func GetVMStatus(name string) (VMStatus, error) {
	socketPath := "/var/lib/incus/unix.socket"
	var status VMStatus

	// Connect to local Incus daemon
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return status, fmt.Errorf("Incus Unix socket bağlantı hatası: %w", err)
	}

	// Fetch static instance configuration and creation date
	inst, _, err := c.GetInstance(name)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return status, fmt.Errorf("%s adında bir makine bulunamadı", name)
		}
		return status, fmt.Errorf("makine bilgisi alınamadı: %w", err)
	}

	status.Name = inst.Name
	status.Type = inst.Type
	status.CreatedAt = inst.CreatedAt.Format("2006-01-02 15:04:05")

	// Get limits from configuration
	cpuLimit := inst.Config["limits.cpu"]
	if cpuLimit == "" {
		cpuLimit = inst.ExpandedConfig["limits.cpu"]
	}
	if cpuLimit == "" {
		cpuLimit = "Sınırsız (default)"
	}
	status.CPULimit = cpuLimit

	ramLimit := inst.Config["limits.memory"]
	if ramLimit == "" {
		ramLimit = inst.ExpandedConfig["limits.memory"]
	}
	if ramLimit == "" {
		ramLimit = "Sınırsız (default)"
	}
	status.RAMLimit = ramLimit

	// Fetch dynamic instance state for status and IP Address
	state, _, err := c.GetInstanceState(name)
	if err != nil {
		// Fallback to static status if GetInstanceState fails for some reason
		status.Status = strings.ToLower(inst.Status)
		status.IPAddress = "N/A"
	} else {
		status.Status = strings.ToLower(state.Status)

		// Parse IP addresses
		var ips []string
		for ifaceName, iface := range state.Network {
			if ifaceName == "lo" {
				continue
			}
			for _, addr := range iface.Addresses {
				// Prioritize global IPv4 addresses
				if addr.Family == "inet" && addr.Scope == "global" {
					ips = append(ips, addr.Address)
				}
			}
		}
		// If no global IPv4 is found, fallback to any IPv4/IPv6 except loopback
		if len(ips) == 0 {
			for ifaceName, iface := range state.Network {
				if ifaceName == "lo" {
					continue
				}
				for _, addr := range iface.Addresses {
					ips = append(ips, addr.Address)
				}
			}
		}

		if len(ips) > 0 {
			status.IPAddress = strings.Join(ips, ", ")
		} else {
			status.IPAddress = "N/A"
		}
	}

	return status, nil
}

type CreateVMRequest struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	CPU   int    `json:"cpu"`
	RAM   int    `json:"ram"`
}

func handleGetVMs(w http.ResponseWriter, r *http.Request) {
	socketPath := "/var/lib/incus/unix.socket"

	// If it is an HTMX request, we return the HTML dashboard fragment.
	if r.Header.Get("HX-Request") == "true" {
		c, err := incus.ConnectIncusUnix(socketPath, nil)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			templates.ExecuteTemplate(w, "error.html", nil)
			return
		}

		instances, err := c.GetInstances(api.InstanceTypeAny)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			templates.ExecuteTemplate(w, "error.html", nil)
			return
		}

		type DashboardInstance struct {
			Name      string
			Type      string
			Status    string
			CPULimit  string
			RAMLimit  string
			CreatedAt string
		}

		var dashboardInstances []DashboardInstance
		runningCount := 0

		for _, inst := range instances {
			statusStr := strings.ToLower(inst.Status)
			if statusStr == "running" {
				runningCount++
			}

			cpuLimit := inst.Config["limits.cpu"]
			if cpuLimit == "" {
				cpuLimit = inst.ExpandedConfig["limits.cpu"]
			}
			if cpuLimit == "" {
				cpuLimit = "1"
			}

			ramLimit := inst.Config["limits.memory"]
			if ramLimit == "" {
				ramLimit = inst.ExpandedConfig["limits.memory"]
			}
			if ramLimit == "" {
				ramLimit = "1GiB"
			}

			dashboardInstances = append(dashboardInstances, DashboardInstance{
				Name:      inst.Name,
				Type:      inst.Type,
				Status:    statusStr,
				CPULimit:  cpuLimit,
				RAMLimit:  ramLimit,
				CreatedAt: inst.CreatedAt.Format("2006-01-02 15:04"),
			})
		}

		ramPercent, ramFormatted, err := getHostMemoryUsage()
		if err != nil {
			ramPercent = 0
			ramFormatted = "N/A"
		}

		diskPercent, diskFormatted, err := getHostDiskUsage()
		if err != nil {
			diskPercent = 0
			diskFormatted = "N/A"
		}

		// Query last backup time
		var lastBackupStr string = "Hiç yedek yok"
		var lastBackupTime string
		err = db.QueryRow("SELECT timestamp FROM system_logs WHERE action = 'Yedekleme' ORDER BY id DESC LIMIT 1").Scan(&lastBackupTime)
		if err == nil && lastBackupTime != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", lastBackupTime); err == nil {
				lastBackupStr = t.Format("15:04")
			} else if t, err := time.Parse(time.RFC3339, lastBackupTime); err == nil {
				lastBackupStr = t.Format("15:04")
			} else {
				lastBackupStr = lastBackupTime
			}
		}

		// Query last 20 system audit logs
		rows, err := db.Query("SELECT id, datetime(timestamp, 'localtime'), user, action, target, status FROM system_logs ORDER BY id DESC LIMIT 20")
		var logs []SystemLog
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var l SystemLog
				if err := rows.Scan(&l.ID, &l.Timestamp, &l.User, &l.Action, &l.Target, &l.Status); err == nil {
					logs = append(logs, l)
				}
			}
		}

		data := struct {
			RunningCount  int
			TotalCount    int
			RAMPercent    int
			RAMFormatted  string
			DiskPercent   int
			DiskFormatted string
			LastBackup    string
			Instances     []DashboardInstance
			Logs          []SystemLog
		}{
			RunningCount:  runningCount,
			TotalCount:    len(instances),
			RAMPercent:    ramPercent,
			RAMFormatted:  ramFormatted,
			DiskPercent:   diskPercent,
			DiskFormatted: diskFormatted,
			LastBackup:    lastBackupStr,
			Instances:     dashboardInstances,
			Logs:          logs,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		templates.ExecuteTemplate(w, "dashboard.html", data)
		return
	}

	// Default behavior: return JSON
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	instances, err := c.GetInstances(api.InstanceTypeAny)
	if err != nil {
		http.Error(w, fmt.Sprintf("Örnekler alınamadı: %v", err), http.StatusInternalServerError)
		return
	}

	var result []InstanceInfo
	for _, inst := range instances {
		result = append(result, InstanceInfo{
			Name:   inst.Name,
			Status: inst.Status,
			Type:   inst.Type,
		})
	}
	if result == nil {
		result = []InstanceInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleGetVMDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "İsim parametresi eksik", http.StatusBadRequest)
		return
	}

	status, err := GetVMStatus(name)
	if err != nil {
		if strings.Contains(err.Error(), "bulunamadı") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleCreateVM(w http.ResponseWriter, r *http.Request) {
	// If it is an HTMX request, we parse the form values and return HTML wizard responses.
	if r.Header.Get("HX-Request") == "true" {
		name := r.FormValue("name")
		image := r.FormValue("image")
		cpuStr := r.FormValue("cpu")
		ramStr := r.FormValue("ram")

		cpu := 2
		ram := 4
		fmt.Sscanf(cpuStr, "%d", &cpu)
		fmt.Sscanf(ramStr, "%d", &ram)

		err := CreateVM(name, image, cpu, ram)
		if err != nil {
			// If creation fails, we return step 3 template with the error message
			data := struct {
				Name  string
				Image string
				CPU   int
				RAM   int
				Error string
			}{
				Name:  name,
				Image: image,
				CPU:   cpu,
				RAM:   ram,
				Error: err.Error(),
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK) // HTMX expects 200 to swap in error block smoothly
			templates.ExecuteTemplate(w, "wizard-step3.html", data)
			return
		}

		// Success! Close modal and trigger custom event
		LogSystemEvent(getUsername(r), "Oluşturma", name, "Başarılı")
		w.Header().Set("HX-Trigger", "vms-updated")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		templates.ExecuteTemplate(w, "wizard-success.html", nil)
		return
	}

	// Default behavior: return JSON
	var req CreateVMRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Geçersiz JSON içeriği", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name alanı zorunludur", http.StatusBadRequest)
		return
	}
	if req.Image == "" {
		req.Image = "images:ubuntu/22.04"
	}
	if req.CPU <= 0 {
		req.CPU = 1
	}
	if req.RAM <= 0 {
		req.RAM = 1
	}

	err = CreateVM(req.Name, req.Image, req.CPU, req.RAM)
	if err != nil {
		LogSystemEvent(getUsername(r), "Oluşturma", req.Name, "Başarısız")
		http.Error(w, fmt.Sprintf("VM oluşturulamadı: %v", err), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Oluşturma", req.Name, "Başarılı")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("VM '%s' başarıyla oluşturuldu", req.Name),
	})
}

func handleStopVM(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "İsim parametresi eksik", http.StatusBadRequest)
		return
	}

	err := StopVM(name)
	if err != nil {
		if strings.Contains(err.Error(), "bulunamadı") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Durdurma", name, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("VM '%s' durduruldu", name),
	})
}

func handleStartVM(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "İsim parametresi eksik", http.StatusBadRequest)
		return
	}

	err := StartVM(name)
	if err != nil {
		if strings.Contains(err.Error(), "bulunamadı") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Başlatma", name, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("VM '%s' başlatıldı", name),
	})
}

func handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "İsim parametresi eksik", http.StatusBadRequest)
		return
	}

	err := DeleteVM(name)
	if err != nil {
		if strings.Contains(err.Error(), "bulunamadı") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Silme", name, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("VM '%s' silindi", name),
	})
}

func handleWizardStep1(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	image := r.FormValue("image")

	data := struct {
		Name  string
		Image string
	}{
		Name:  name,
		Image: image,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.ExecuteTemplate(w, "wizard-step1.html", data)
}

func handleWizardStep2(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	image := r.FormValue("image")

	data := struct {
		Name  string
		Image string
	}{
		Name:  name,
		Image: image,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.ExecuteTemplate(w, "wizard-step2.html", data)
}

func handleWizardStep3(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	image := r.FormValue("image")
	cpuStr := r.FormValue("cpu")
	ramStr := r.FormValue("ram")

	cpu := 2
	ram := 4
	fmt.Sscanf(cpuStr, "%d", &cpu)
	fmt.Sscanf(ramStr, "%d", &ram)

	data := struct {
		Name  string
		Image string
		CPU   int
		RAM   int
		Error string
	}{
		Name:  name,
		Image: image,
		CPU:   cpu,
		RAM:   ram,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.ExecuteTemplate(w, "wizard-step3.html", data)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}

	data := struct {
		Hostname string
	}{
		Hostname: hostname,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = templates.ExecuteTemplate(w, "layout.html", data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Şablon oluşturma hatası: %v", err), http.StatusInternalServerError)
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type WSReadWriteCloser struct {
	conn   *websocket.Conn
	reader io.Reader
}

func NewWSReadWriteCloser(conn *websocket.Conn) *WSReadWriteCloser {
	return &WSReadWriteCloser{conn: conn}
}

func (w *WSReadWriteCloser) Read(p []byte) (int, error) {
	for {
		if w.reader == nil {
			messageType, r, err := w.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
				continue
			}
			w.reader = r
		}
		n, err := w.reader.Read(p)
		if err == io.EOF {
			w.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *WSReadWriteCloser) Write(p []byte) (int, error) {
	err := w.conn.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSReadWriteCloser) Close() error {
	return w.conn.Close()
}

func handleGetConsole(w http.ResponseWriter, r *http.Request) {
	vmName := r.PathValue("vm_name")
	if vmName == "" {
		http.Error(w, "VM ismi eksik", http.StatusBadRequest)
		return
	}

	data := struct {
		VMName string
	}{
		VMName: vmName,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := templates.ExecuteTemplate(w, "console.html", data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Şablon oluşturma hatası: %v", err), http.StatusInternalServerError)
	}
}

func handleConsoleWS(w http.ResponseWriter, r *http.Request) {
	vmName := r.PathValue("vm_name")
	if vmName == "" {
		http.Error(w, "VM ismi eksik", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "Incus bağlantı hatası"))
		return
	}

	disconnectChan := make(chan bool, 1)
	defer func() {
		select {
		case disconnectChan <- true:
		default:
		}
	}()

	wsWrapper := NewWSReadWriteCloser(conn)

	consolePost := api.InstanceConsolePost{
		Type: "vga",
	}

	consoleArgs := incus.InstanceConsoleArgs{
		Terminal:          wsWrapper,
		ConsoleDisconnect: disconnectChan,
	}

	op, err := c.ConsoleInstance(vmName, consolePost, &consoleArgs)
	if err != nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}

	err = op.Wait()
	if err != nil {
		// Log or handle wait error
	}
}

func main() {
	// Check for the "serve" subcommand
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		InitDB()
		defer db.Close()
		InitNetworkDB()
		InitQosDB()
		InitSecurityDB()
		InitSettingsDB()

		// Start ZFS auto-snapshot scheduler
		StartAutoSnapshotScheduler()

		mux := http.NewServeMux()
		mux.HandleFunc("GET /", handleIndex)
		mux.Handle("GET /static/", http.FileServer(http.FS(files)))
		mux.HandleFunc("GET /login", handleGetLogin)
		mux.HandleFunc("POST /api/login", handlePostLogin)
		mux.HandleFunc("GET /logout", handleLogout)
		mux.HandleFunc("GET /console/{vm_name}", handleGetConsole)
		mux.HandleFunc("GET /ws/console/{vm_name}", handleConsoleWS)
		mux.HandleFunc("GET /api/vms", handleGetVMs)
		mux.HandleFunc("GET /api/vms/{name}", handleGetVMDetail)
		mux.HandleFunc("POST /api/vms", handleCreateVM)
		mux.HandleFunc("POST /api/vms/{name}/stop", handleStopVM)
		mux.HandleFunc("POST /api/vms/{name}/start", handleStartVM)
		mux.HandleFunc("DELETE /api/vms/{name}", handleDeleteVM)
		mux.HandleFunc("POST /api/vms/{name}/hardware", handleUpdateVMHardware)
		mux.HandleFunc("GET /api/wizard/step1", handleWizardStep1)
		mux.HandleFunc("POST /api/wizard/step1", handleWizardStep1)
		mux.HandleFunc("POST /api/wizard/step2", handleWizardStep2)
		mux.HandleFunc("POST /api/wizard/step3", handleWizardStep3)

		// ZFS Snapshot management endpoints
		mux.HandleFunc("GET /api/snapshots", handleGetSnapshots)
		mux.HandleFunc("POST /api/snapshots", handleCreateSnapshotAPI)
		mux.HandleFunc("POST /api/snapshots/{id}/rollback", handleRollbackSnapshotAPI)
		mux.HandleFunc("GET /api/backup/page", handleGetBackupPage)
		mux.HandleFunc("POST /api/snapshots/destroy", handleDestroySnapshotAPI)
		mux.HandleFunc("GET /api/vms/{name}/detail", handleGetVMDetailHTML)

		// OVS Network Segmentation endpoints
		mux.HandleFunc("GET /api/network/segments", handleGetSegments)
		mux.HandleFunc("POST /api/network/segments", handleCreateSegmentAPI)
		mux.HandleFunc("POST /api/network/segments/{name}/assign", handleAssignVMAPI)

		// Traffic Prioritization (QoS) endpoints
		mux.HandleFunc("GET /api/qos/rules", handleGetQoSRules)
		mux.HandleFunc("POST /api/qos/rules", handleCreateQoSRule)

		// Security endpoints
		mux.HandleFunc("GET /api/security/status", handleGetSecurityStatus)
		mux.HandleFunc("POST /api/security/toggle", handleToggleSecurity)
		mux.HandleFunc("GET /api/security/page", handleGetSecurityPage)

		// Settings endpoints
		mux.HandleFunc("GET /api/settings/page", handleGetSettingsPage)
		mux.HandleFunc("POST /api/settings/password", handlePostSettingsPassword)
		mux.HandleFunc("POST /api/settings/session", handlePostSettingsSession)
		mux.HandleFunc("POST /api/settings/2fa/enable", handlePostSettings2FAEnable)
		mux.HandleFunc("POST /api/settings/2fa/disable", handlePostSettings2FADisable)

		// System Updates endpoints
		mux.HandleFunc("GET /api/updates/check", handleGetUpdatesCheck)
		mux.HandleFunc("POST /api/updates/start", handleStartUpdate)
		mux.HandleFunc("GET /api/updates/status", handleGetUpdateStatus)
		mux.HandleFunc("POST /api/updates/reset", handleResetUpdate)

		fmt.Println("REST API sunucusu 0.0.0.0:8080 adresinde başlatılıyor...")
		err := http.ListenAndServe("0.0.0.0:8080", AuthMiddleware(mux))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: Sunucu başlatılamadı. Detay: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Check for the "create" subcommand
	if len(os.Args) > 1 && os.Args[1] == "create" {
		createCmd := flag.NewFlagSet("create", flag.ExitOnError)
		nameFlag := createCmd.String("name", "", "Sanal makine adı (zorunlu)")
		cpuFlag := createCmd.Int("cpu", 1, "CPU sayısı")
		ramFlag := createCmd.Int("ram", 1, "Bellek boyutu (GiB)")
		imageFlag := createCmd.String("image", "images:ubuntu/22.04", "Kullanılacak imaj")

		err := createCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: Argümanlar ayrıştırılamadı. Detay: %v\n", err)
			os.Exit(1)
		}

		if *nameFlag == "" {
			fmt.Fprintln(os.Stderr, "Hata: --name parametresi zorunludur.")
			createCmd.Usage()
			os.Exit(1)
		}

		fmt.Printf("VM '%s' oluşturuluyor (İmaj: %s, CPU: %d, RAM: %dGiB)...\n", *nameFlag, *imageFlag, *cpuFlag, *ramFlag)
		err = CreateVM(*nameFlag, *imageFlag, *cpuFlag, *ramFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: VM oluşturulurken hata oluştu. Detay: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Başarılı: VM '%s' başarıyla oluşturuldu!\n", *nameFlag)
		return
	}

	// Check for the "stop" subcommand
	if len(os.Args) > 1 && os.Args[1] == "stop" {
		stopCmd := flag.NewFlagSet("stop", flag.ExitOnError)
		nameFlag := stopCmd.String("name", "", "Durdurulacak sanal makine adı (zorunlu)")

		err := stopCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: Argümanlar ayrıştırılamadı. Detay: %v\n", err)
			os.Exit(1)
		}

		if *nameFlag == "" {
			fmt.Fprintln(os.Stderr, "Hata: --name parametresi zorunludur.")
			stopCmd.Usage()
			os.Exit(1)
		}

		fmt.Printf("VM '%s' durduruluyor...\n", *nameFlag)
		err = StopVM(*nameFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Başarılı: VM '%s' durduruldu.\n", *nameFlag)
		return
	}

	// Check for the "start" subcommand
	if len(os.Args) > 1 && os.Args[1] == "start" {
		startCmd := flag.NewFlagSet("start", flag.ExitOnError)
		nameFlag := startCmd.String("name", "", "Başlatılacak sanal makine adı (zorunlu)")

		err := startCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: Argümanlar ayrıştırılamadı. Detay: %v\n", err)
			os.Exit(1)
		}

		if *nameFlag == "" {
			fmt.Fprintln(os.Stderr, "Hata: --name parametresi zorunludur.")
			startCmd.Usage()
			os.Exit(1)
		}

		fmt.Printf("VM '%s' başlatılıyor...\n", *nameFlag)
		err = StartVM(*nameFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Başarılı: VM '%s' başlatıldı.\n", *nameFlag)
		return
	}

	// Check for the "status" subcommand
	if len(os.Args) > 1 && os.Args[1] == "status" {
		statusCmd := flag.NewFlagSet("status", flag.ExitOnError)
		nameFlag := statusCmd.String("name", "", "Durumu sorgulanacak sanal makine adı (zorunlu)")

		err := statusCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: Argümanlar ayrıştırılamadı. Detay: %v\n", err)
			os.Exit(1)
		}

		if *nameFlag == "" {
			fmt.Fprintln(os.Stderr, "Hata: --name parametresi zorunludur.")
			statusCmd.Usage()
			os.Exit(1)
		}

		status, err := GetVMStatus(*nameFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Hata: %v\n", err)
			os.Exit(1)
		}

		// Print output in a clean, human-readable table/key-value format
		fmt.Println("--------------------------------------------------------")
		fmt.Printf("| %-20s | %-29s |\n", "PARAMETRE", "DEĞER")
		fmt.Println("--------------------------------------------------------")
		fmt.Printf("| %-20s | %-29s |\n", "İsim", status.Name)
		fmt.Printf("| %-20s | %-29s |\n", "Durum", status.Status)
		fmt.Printf("| %-20s | %-29s |\n", "CPU Limiti", status.CPULimit)
		fmt.Printf("| %-20s | %-29s |\n", "RAM Limiti", status.RAMLimit)
		fmt.Printf("| %-20s | %-29s |\n", "IP Adresi", status.IPAddress)
		fmt.Printf("| %-20s | %-29s |\n", "Oluşturulma Tarihi", status.CreatedAt)
		fmt.Println("--------------------------------------------------------")
		return
	}

	// Default behavior: List instances as JSON
	socketPath := "/var/lib/incus/unix.socket"

	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Hata: Incus servisi çalışmıyor veya socket yolu hatalı. Detay: %v\n", err)
		os.Exit(1)
	}

	// Fetch all instances (both VMs and containers)
	instances, err := c.GetInstances(api.InstanceTypeAny)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Hata: Örnekler (instances) alınamadı. Detay: %v\n", err)
		os.Exit(1)
	}

	// Map to simplified structure for JSON output
	var result []InstanceInfo
	for _, inst := range instances {
		result = append(result, InstanceInfo{
			Name:   inst.Name,
			Status: inst.Status,
			Type:   inst.Type,
		})
	}

	// Initialize to empty slice if nil to ensure valid JSON array [] instead of null
	if result == nil {
		result = []InstanceInfo{}
	}

	// Marshal to JSON with pretty print and write to terminal
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Hata: JSON formatına dönüştürülemedi. Detay: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(jsonBytes))
}

// POST /api/vms/{name}/hardware
func handleUpdateVMHardware(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Yalnızca POST istekleri desteklenir", http.StatusMethodNotAllowed)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "VM ismi eksik", http.StatusBadRequest)
		return
	}

	cpuStr := r.FormValue("cpu")
	ramStr := r.FormValue("ram")

	var cpu int
	var ram int
	if _, err := fmt.Sscanf(cpuStr, "%d", &cpu); err != nil || cpu <= 0 {
		http.Error(w, "Geçersiz CPU değeri", http.StatusBadRequest)
		return
	}
	if _, err := fmt.Sscanf(ramStr, "%d", &ram); err != nil || ram <= 0 {
		http.Error(w, "Geçersiz RAM değeri", http.StatusBadRequest)
		return
	}

	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Incus bağlantı hatası: %v", err), http.StatusInternalServerError)
		return
	}

	inst, Etag, err := c.GetInstance(name)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM bulunamadı: %v", err), http.StatusNotFound)
		return
	}

	// Verify VM is stopped before updating hardware configurations
	if strings.ToLower(inst.Status) == "running" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`<div class="alert-danger" style="padding: 10px; background-color: #FEF2F2; color: #DC2626; border: 1px solid #FCA5A5; border-radius: 4px; margin-bottom: 15px;">Sanal makine çalışırken donanım limitleri değiştirilemez. Lütfen önce durdurun.</div>`))
		return
	}

	if inst.Config == nil {
		inst.Config = make(map[string]string)
	}
	inst.Config["limits.cpu"] = strconv.Itoa(cpu)
	inst.Config["limits.memory"] = fmt.Sprintf("%dGiB", ram)

	op, err := c.UpdateInstance(name, inst.Writable(), Etag)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`<div class="alert-danger" style="padding: 10px; background-color: #FEF2F2; color: #DC2626; border: 1px solid #FCA5A5; border-radius: 4px; margin-bottom: 15px;">Donanım güncellenemedi: %v</div>`, err)))
		return
	}

	err = op.Wait()
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`<div class="alert-danger" style="padding: 10px; background-color: #FEF2F2; color: #DC2626; border: 1px solid #FCA5A5; border-radius: 4px; margin-bottom: 15px;">Incus işlemi tamamlanamadı: %v</div>`, err)))
		return
	}

	LogSystemEvent(getUsername(r), "Donanım Güncelleme", fmt.Sprintf("%s (CPU: %d, RAM: %dGB)", name, cpu, ram), "Başarılı")

	w.Header().Set("HX-Trigger", "vms-updated")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<div class="alert-success" style="padding: 10px; background-color: #ECFDF5; color: #059669; border: 1px solid #A7F3D0; border-radius: 4px; margin-bottom: 15px;">Donanım başarıyla güncellendi.</div>`))
}
