package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
)

// RawDisk represents an unmounted, raw block device on the host
type RawDisk struct {
	Name       string    `json:"name"`
	Size       string    `json:"size"`
	Type       string    `json:"type"`
	Mountpoint string    `json:"mountpoint"`
	Fstype     string    `json:"fstype"`
	Children   []RawDisk `json:"children,omitempty"`
}

// LsblkOutput is the JSON structure returned by lsblk
type LsblkOutput struct {
	Blockdevices []RawDisk `json:"blockdevices"`
}

// ListRawDisks lists all unmounted physical disks/partitions
func ListRawDisks() ([]RawDisk, error) {
	cmd := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TYPE,MOUNTPOINT,FSTYPE")
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
			rawDisks = append(rawDisks, RawDisk{
				Name:       dev.Name,
				Size:       dev.Size,
				Type:       dev.Type,
				Mountpoint: dev.Mountpoint,
			})
		}
	}
	return rawDisks, nil
}

// CreateDatastore formats a disk as a ZFS pool and adds it to Incus
func CreateDatastore(poolName, diskName string) error {
	diskPath := fmt.Sprintf("/dev/%s", diskName)

	// 1. Mevcut dosya sistemlerini silmek için disk temizliği (wipefs) ve ZFS havuzu oluştur (-f ile zorla)
	exec.Command("wipefs", "-a", diskPath).Run() // Önce temizlemeyi deneriz
	zpoolCmd := exec.Command("zpool", "create", "-f", poolName, diskPath)
	if out, err := zpoolCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ZFS havuzu oluşturulamadı: %w. Detay: %s", err, string(out))
	}

	// 2. Incus'a ZFS havuzunu bağla
	incusCmd := exec.Command("incus", "storage", "create", poolName, "zfs", fmt.Sprintf("source=%s", poolName))
	if out, err := incusCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Incus storage eklenemedi: %w. Detay: %s", err, string(out))
	}

	return nil
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
		Pools []IncusStoragePool
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
		LogSystemEvent(getUsername(r), "Datastore Oluşturma", poolName, "Başarısız")
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

type IncusStoragePool struct {
	Name        string   `json:"name"`
	Driver      string   `json:"driver"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	UsedBy      []string `json:"used_by"`
}

// ListDatastores returns all storage pools from Incus
func ListDatastores() ([]IncusStoragePool, error) {
	cmd := exec.Command("incus", "storage", "list", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Incus storage list başarısız: %w", err)
	}

	var pools []IncusStoragePool
	if err := json.Unmarshal(out, &pools); err != nil {
		return nil, err
	}
	return pools, nil
}

// handleDeleteDatastoreAPI removes a datastore
func handleDeleteDatastoreAPI(w http.ResponseWriter, r *http.Request) {
	poolName := r.PathValue("pool")
	if poolName == "" || poolName == "default" {
		http.Error(w, "Geçersiz veya silinemez havuz adı", http.StatusBadRequest)
		return
	}

	// 1. Delete from Incus
	incusCmd := exec.Command("incus", "storage", "delete", poolName)
	if out, err := incusCmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "Datastore Silme", poolName, "Başarısız")
		http.Error(w, fmt.Sprintf("Incus storage silinemedi: %s", string(out)), http.StatusInternalServerError)
		return
	}

	// 2. Destroy ZFS pool to free disk
	zpoolCmd := exec.Command("zpool", "destroy", poolName)
	if out, err := zpoolCmd.CombinedOutput(); err != nil {
		fmt.Printf("Zpool destroy uyarı: %v - %s\n", err, string(out))
		// ZFS silinirken hata olsa bile devam edelim, diskin elle temizlenmesi gerekebilir
	}

	LogSystemEvent(getUsername(r), "Datastore Silme", poolName, "Başarılı")
	w.WriteHeader(http.StatusOK)
}
