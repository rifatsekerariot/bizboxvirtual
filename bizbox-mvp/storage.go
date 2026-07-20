package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
)

// RawDisk represents an unmounted, raw block device on the host
type RawDisk struct {
	Name       string `json:"name"`
	Size       string `json:"size"`
	Type       string `json:"type"`
	Mountpoint string `json:"mountpoint"`
}

// LsblkOutput is the JSON structure returned by lsblk
type LsblkOutput struct {
	Blockdevices []RawDisk `json:"blockdevices"`
}

// ListRawDisks lists all unmounted physical disks/partitions
func ListRawDisks() ([]RawDisk, error) {
	// List block devices: JSON format, do not print deps, no headings, specific columns
	cmd := exec.Command("lsblk", "-J", "-d", "-n", "-o", "NAME,SIZE,TYPE,MOUNTPOINT")
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
		if dev.Mountpoint == "" || dev.Mountpoint == "null" {
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

	// 1. ZFS Pool oluştur
	zpoolCmd := exec.Command("zpool", "create", poolName, diskPath)
	if out, err := zpoolCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ZFS havuzu oluşturulamadı: %w. Detay: %s", err, string(out))
	}

	// 2. Incus'a ZFS havuzunu bağla
	incusCmd := exec.Command("incus", "admin", "storage", "create", poolName, "zfs", fmt.Sprintf("source=%s", poolName))
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

	data := struct {
		Disks []RawDisk
	}{
		Disks: disks,
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
