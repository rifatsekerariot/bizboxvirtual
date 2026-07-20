package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// ListTemplates returns instances marked as templates
func ListTemplates() ([]VMStatus, error) {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return nil, fmt.Errorf("Incus bağlantı hatası: %w", err)
	}

	instances, err := c.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("Şablonlar listelenemedi: %w", err)
	}

	var templates []VMStatus
	for _, inst := range instances {
		// Sadece user.template = true olanları şablon olarak kabul et
		if inst.Config["user.template"] == "true" || inst.ExpandedConfig["user.template"] == "true" {
			status, _ := GetVMStatus(inst.Name)
			templates = append(templates, status)
		}
	}
	return templates, nil
}

// MarkAsTemplate marks an existing VM as a template
func MarkAsTemplate(name string, isTemplate bool) error {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus bağlantı hatası: %w", err)
	}

	inst, Etag, err := c.GetInstance(name)
	if err != nil {
		return fmt.Errorf("VM bulunamadı: %w", err)
	}

	if inst.Config == nil {
		inst.Config = make(map[string]string)
	}

	if isTemplate {
		// Stop if running
		if strings.ToLower(inst.Status) == "running" {
			return fmt.Errorf("Sanal makine çalışırken şablona dönüştürülemez. Lütfen önce durdurun")
		}
		inst.Config["user.template"] = "true"
	} else {
		delete(inst.Config, "user.template")
	}

	op, err := c.UpdateInstance(name, inst.Writable(), Etag)
	if err != nil {
		return fmt.Errorf("VM güncellenemedi: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("VM güncelleme tamamlanamadı: %w", err)
	}

	return nil
}

// CloneTemplate creates a new VM from a template
func CloneTemplate(templateName, newName string) error {
	socketPath := "/var/lib/incus/unix.socket"
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return fmt.Errorf("Incus bağlantı hatası: %w", err)
	}

	source := api.InstanceSource{
		Type:   "copy",
		Source: templateName,
	}

	req := api.InstancesPost{
		Name:   newName,
		Source: source,
	}

	op, err := c.CreateInstance(req)
	if err != nil {
		return fmt.Errorf("Klonlama başlatılamadı: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("Klonlama hatası: %w", err)
	}

	// Remove user.template flag from the newly cloned instance
	inst, Etag, err := c.GetInstance(newName)
	if err == nil {
		if inst.Config != nil {
			delete(inst.Config, "user.template")
			op, _ = c.UpdateInstance(newName, inst.Writable(), Etag)
			if op != nil {
				op.Wait()
			}
		}
	}

	return nil
}

// Handlers

func handleGetTemplatesPage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		templatesList, err := ListTemplates()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		data := struct {
			Templates []VMStatus
		}{
			Templates: templatesList,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err = templates.ExecuteTemplate(w, "templates.html", data)
		if err != nil {
			http.Error(w, fmt.Sprintf("Şablon oluşturulamadı: %v", err), http.StatusInternalServerError)
		}
		return
	}

	// If not an HTMX request, redirect to the dashboard
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleMarkAsTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Yalnızca POST istekleri desteklenir", http.StatusMethodNotAllowed)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "VM ismi eksik", http.StatusBadRequest)
		return
	}

	err := MarkAsTemplate(name, true)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	LogSystemEvent(getUsername(r), "Şablona Çevir", name, "Başarılı")
	json.NewEncoder(w).Encode(map[string]string{"message": "Sanal makine şablona dönüştürüldü"})
}

func handleCloneTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Yalnızca POST istekleri desteklenir", http.StatusMethodNotAllowed)
		return
	}

	templateName := r.PathValue("name")
	if templateName == "" {
		http.Error(w, "Şablon ismi eksik", http.StatusBadRequest)
		return
	}

	newName := r.FormValue("new_name")
	if newName == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`<div class="alert alert-danger" style="color:#DC2626; background:#FEF2F2; padding:12px; border-radius:6px;">Yeni isim boş olamaz</div>`))
		return
	}

	err := CloneTemplate(templateName, newName)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`<div class="alert alert-danger" style="color:#DC2626; background:#FEF2F2; padding:12px; border-radius:6px;">Klonlama hatası: %v</div>`, err)))
		return
	}

	LogSystemEvent(getUsername(r), "Klonlama", templateName, fmt.Sprintf("Yeni Makine: %s", newName))
	
	// HTMX trigger
	w.Header().Set("HX-Trigger", "vmsChanged")
	w.Write([]byte(`<div class="alert alert-success" style="color:#059669; background:#D1FAE5; padding:12px; border-radius:6px;">Şablon başarıyla klonlandı.</div>`))
}
