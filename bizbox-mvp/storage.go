package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// RawDisk represents an unmounted, raw block device on the host
type RawDisk struct {
	Name       string      `json:"name"`
	Size       string      `json:"size"`
	Type       string      `json:"type"`
	Mountpoint string      `json:"mountpoint"`
	Fstype     string      `json:"fstype"`
	Model      string      `json:"model"`
	Serial     string      `json:"serial"`
	Rota       interface{} `json:"rota"`
	MediaType  string      `json:"media_type"`
	Children   []RawDisk   `json:"children,omitempty"`
}

// LsblkOutput is the JSON structure returned by lsblk
type LsblkOutput struct {
	Blockdevices []RawDisk `json:"blockdevices"`
}

// DatastoreInfo represents an ESXi-style datastore metric summary
type DatastoreInfo struct {
	Name        string `json:"name"`
	Driver      string `json:"driver"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	TotalBytes  uint64 `json:"total_bytes"`
	UsedBytes   uint64 `json:"used_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	UsedPercent int    `json:"used_percent"`
	TotalStr    string `json:"total_str"`
	UsedStr     string `json:"used_str"`
	FreeStr     string `json:"free_str"`
	UsedVMs     int    `json:"used_vms"`
	IsSystem    bool   `json:"is_system"`
}

func formatBytes(b uint64) string {
	if b == 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ListRawDisks lists all unmounted physical disks/partitions
func ListRawDisks() ([]RawDisk, error) {
	cmd := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TYPE,MOUNTPOINT,FSTYPE,MODEL,SERIAL,ROTA")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk calistirilamadi: %w", err)
	}

	var data LsblkOutput
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("lsblk ciktisi parse edilemedi: %w", err)
	}

	var rawDisks []RawDisk
	for _, dev := range data.Blockdevices {
		if dev.Type == "rom" || dev.Type == "loop" {
			continue
		}

		mediaType := "HDD (HDD)"
		if strings.HasPrefix(dev.Name, "nvme") {
			mediaType = "NVMe (Flash)"
		} else if dev.Rota == false || fmt.Sprintf("%v", dev.Rota) == "0" {
			mediaType = "SSD (Flash)"
		}
		dev.MediaType = mediaType

		isMounted := dev.Mountpoint != "" && dev.Mountpoint != "null"
		isZfs := dev.Fstype == "zfs_member"

		for _, child := range dev.Children {
			if child.Mountpoint != "" && child.Mountpoint != "null" {
				isMounted = true
			}
			if child.Fstype == "zfs_member" {
				isZfs = true
			}
		}

		if !isMounted && !isZfs {
			rawDisks = append(rawDisks, dev)
		}
	}
	return rawDisks, nil
}

// CreateDatastore formats a disk as a ZFS pool and adds it to Incus via API
func CreateDatastore(poolName, diskName string) error {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus client error: %v", err)
	}

	diskPath := fmt.Sprintf("/dev/%s", diskName)

	// Wipe the disk to ensure Incus doesn't fail due to existing signatures
	exec.Command("wipefs", "-a", diskPath).Run()

	req := api.StoragePoolsPost{
		StoragePoolPut: api.StoragePoolPut{
			Config: map[string]string{
				"source": diskPath,
			},
			Description: "ESXi Datastore",
		},
		Driver: "zfs",
		Name:   poolName,
	}

	err = c.CreateStoragePool(req)
	if err != nil {
		return fmt.Errorf("Incus storage pool oluşturulamadı: %w", err)
	}

	return nil
}

// ListDatastores returns all storage pools with resource metrics
func ListDatastores() ([]DatastoreInfo, error) {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return nil, fmt.Errorf("Incus client error: %v", err)
	}

	allPools, err := c.GetStoragePools()
	if err != nil {
		return nil, fmt.Errorf("Incus storage list başarısız: %w", err)
	}

	var datastores []DatastoreInfo
	for _, p := range allPools {
		res, errRes := c.GetStoragePoolResources(p.Name)
		var totalBytes, usedBytes, freeBytes uint64
		var usedPercent int

		if errRes == nil && res.Space.Total > 0 {
			totalBytes = res.Space.Total
			usedBytes = res.Space.Used
			freeBytes = totalBytes - usedBytes
			usedPercent = int((float64(usedBytes) / float64(totalBytes)) * 100)
		}

		source := p.Config["source"]
		if source == "" {
			source = "Internal Storage"
		}

		isSystem := p.Name == "default" || p.Name == "local" || p.Description == "System Disk"

		info := DatastoreInfo{
			Name:        p.Name,
			Driver:      p.Driver,
			Status:      p.Status,
			Source:      source,
			TotalBytes:  totalBytes,
			UsedBytes:   usedBytes,
			FreeBytes:   freeBytes,
			UsedPercent: usedPercent,
			TotalStr:    formatBytes(totalBytes),
			UsedStr:     formatBytes(usedBytes),
			FreeStr:     formatBytes(freeBytes),
			UsedVMs:     len(p.UsedBy),
			IsSystem:    isSystem,
		}

		datastores = append(datastores, info)
	}

	return datastores, nil
}

// handleGetStoragePage renders the storage management interface
func handleGetStoragePage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "true" {
		http.Error(w, "Yalnızca HTMX istekleri kabul edilir", http.StatusBadRequest)
		return
	}

	disks, err := ListRawDisks()
	if err != nil {
		fmt.Printf("[Storage] diskler okunamadı: %v\n", err)
	}

	pools, err := ListDatastores()
	if err != nil {
		fmt.Printf("[Storage] havuzlar okunamadı: %v\n", err)
	}

	data := struct {
		Disks []RawDisk
		Pools []DatastoreInfo
	}{
		Disks: disks,
		Pools: pools,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	err = templates.ExecuteTemplate(w, "storage.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreateDatastoreAPI handles the creation of a new datastore
func handleCreateDatastoreAPI(w http.ResponseWriter, r *http.Request) {
	poolName := r.FormValue("pool_name")
	diskName := r.FormValue("disk_name")

	if poolName == "" || diskName == "" {
		http.Error(w, "Havuz adı veya disk adı eksik", http.StatusBadRequest)
		return
	}

	if err := CreateDatastore(poolName, diskName); err != nil {
		LogSystemEvent(getUsername(r), "Datastore Oluşturma", poolName, "Başarısız: "+err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Datastore Oluşturma", poolName, "Başarılı")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Datastore başarıyla oluşturuldu ve Incus'a eklendi.",
	})
}

// handleDeleteDatastoreAPI removes a datastore
func handleDeleteDatastoreAPI(w http.ResponseWriter, r *http.Request) {
	poolName := r.PathValue("pool")
	if poolName == "" || poolName == "default" || poolName == "local" {
		http.Error(w, "Geçersiz veya silinemez havuz adı", http.StatusBadRequest)
		return
	}

	c := GetIncusClient()
	if c == nil {
		http.Error(w, "Incus client error", http.StatusInternalServerError)
		return
	}

	pool, _, err := c.GetStoragePool(poolName)
	if err != nil {
		http.Error(w, "Pool bulunamadı", http.StatusNotFound)
		return
	}

	err = c.DeleteStoragePool(poolName)
	if err != nil {
		LogSystemEvent(getUsername(r), "Datastore Silme", poolName, "Başarısız: "+err.Error())
		http.Error(w, fmt.Sprintf("Incus storage silinemedi: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	if pool.Driver == "zfs" {
		exec.Command("zpool", "destroy", poolName).Run()
	}

	LogSystemEvent(getUsername(r), "Datastore Silme", poolName, "Başarılı")
	w.WriteHeader(http.StatusOK)
}
