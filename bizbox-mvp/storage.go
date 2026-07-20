package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
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
		Pools []api.StoragePool
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

// ListDatastores returns all storage pools from Incus, filtering out system default pool
func ListDatastores() ([]api.StoragePool, error) {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return nil, fmt.Errorf("Incus client error: %v", err)
	}

	allPools, err := c.GetStoragePools()
	if err != nil {
		return nil, fmt.Errorf("Incus storage list başarısız: %w", err)
	}

	var datastores []api.StoragePool
	for _, p := range allPools {
		// Hide system or default disk. Usually named "default" or "local" in setups.
		if p.Name == "default" || p.Name == "local" || p.Description == "System Disk" {
			continue
		}
		datastores = append(datastores, p)
	}

	return datastores, nil
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

	// 1. Get pool info to find if it was ZFS to optionally destroy zpool
	pool, _, err := c.GetStoragePool(poolName)
	if err != nil {
		http.Error(w, "Pool bulunamadı", http.StatusNotFound)
		return
	}

	// 2. Delete from Incus
	err = c.DeleteStoragePool(poolName)
	if err != nil {
		LogSystemEvent(getUsername(r), "Datastore Silme", poolName, "Başarısız: "+err.Error())
		http.Error(w, fmt.Sprintf("Incus storage silinemedi: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// 3. Destroy ZFS pool to free disk (Incus sometimes leaves the zpool imported if it was custom created, 
	// but if Incus created it, it might wipe it. We try to destroy it just in case.)
	if pool.Driver == "zfs" {
		exec.Command("zpool", "destroy", poolName).Run()
	}

	LogSystemEvent(getUsername(r), "Datastore Silme", poolName, "Başarılı")
	w.WriteHeader(http.StatusOK)
}
