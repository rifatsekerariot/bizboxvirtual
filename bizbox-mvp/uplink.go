package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

type VSwitchInfo struct {
	Name       string   `json:"name"`
	IsDefault  bool     `json:"is_default"`
	Interfaces []string `json:"interfaces"`
}

type UplinkInfo struct {
	Name       string `json:"name"`
	IsUp       bool   `json:"is_up"`
	MacAddress string `json:"mac_address"`
	VSwitch    string `json:"vswitch"`
}

type NetworkUplinkResponse struct {
	VSwitches []VSwitchInfo `json:"vswitches"`
	Uplinks   []UplinkInfo  `json:"uplinks"`
}

func handleGetUplinks(w http.ResponseWriter, r *http.Request) {
	// 1. Get physical links
	cmd := exec.Command("ip", "-j", "link", "show")
	output, err := cmd.Output()
	if err != nil {
		http.Error(w, "Ağ arayüzleri okunamadı: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var links []struct {
		Ifname    string `json:"ifname"`
		Operstate string `json:"operstate"`
		Address   string `json:"address"`
		LinkType  string `json:"link_type"`
	}
	if err := json.Unmarshal(output, &links); err != nil {
		http.Error(w, "JSON parse hatası: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter valid physical uplinks
	validPhysicalLinks := make(map[string]bool)
	var uplinks []UplinkInfo
	for _, l := range links {
		if l.LinkType == "loopback" || strings.HasPrefix(l.Ifname, "veth") || strings.HasPrefix(l.Ifname, "br-") || strings.HasPrefix(l.Ifname, "vSwitch") || l.Ifname == "ovs-system" || strings.HasPrefix(l.Ifname, "incus") {
			continue
		}
		validPhysicalLinks[l.Ifname] = true
		uplinks = append(uplinks, UplinkInfo{
			Name:       l.Ifname,
			IsUp:       l.Operstate == "UP" || l.Operstate == "UNKNOWN",
			MacAddress: l.Address,
			VSwitch:    "",
		})
	}

	// 2. Get OVS bridges (vSwitches)
	brOut, _ := exec.Command("ovs-vsctl", "list-br").Output()
	bridges := strings.Split(strings.TrimSpace(string(brOut)), "\n")
	
	var vswitches []VSwitchInfo
	for _, br := range bridges {
		br = strings.TrimSpace(br)
		if br == "" {
			continue
		}
		
		portOut, _ := exec.Command("ovs-vsctl", "list-ports", br).Output()
		ports := strings.Split(strings.TrimSpace(string(portOut)), "\n")
		
		var attachedPhys []string
		for _, p := range ports {
			p = strings.TrimSpace(p)
			if validPhysicalLinks[p] {
				attachedPhys = append(attachedPhys, p)
				// Update uplink info
				for i := range uplinks {
					if uplinks[i].Name == p {
						uplinks[i].VSwitch = br
						break
					}
				}
			}
		}

		vswitches = append(vswitches, VSwitchInfo{
			Name:       br,
			IsDefault:  br == "br-int",
			Interfaces: attachedPhys,
		})
	}

	response := NetworkUplinkResponse{
		VSwitches: vswitches,
		Uplinks:   uplinks,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleCreateVSwitch(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "vSwitch adı eksik", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(name, " \t\n\"'") {
		http.Error(w, "Geçersiz vSwitch adı", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("ovs-vsctl", "add-br", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "vSwitch Oluşturma", name, "Başarısız")
		http.Error(w, "vSwitch oluşturulamadı: "+string(out), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "vSwitch Oluşturma", name, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "vSwitch başarıyla oluşturuldu."})
}

func handleDeleteVSwitch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name == "br-int" {
		http.Error(w, "Geçersiz veya silinemez vSwitch", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("ovs-vsctl", "del-br", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "vSwitch Silme", name, "Başarısız")
		http.Error(w, "vSwitch silinemedi: "+string(out), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "vSwitch Silme", name, "Başarılı")
	w.WriteHeader(http.StatusOK)
}

func handleAttachUplink(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	vswitch := r.FormValue("vswitch")
	if iface == "" || vswitch == "" {
		http.Error(w, "Arayüz veya vSwitch adı eksik", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("ovs-vsctl", "add-port", vswitch, iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "Uplink Ekleme", fmt.Sprintf("%s -> %s", iface, vswitch), "Başarısız")
		http.Error(w, "Arayüz eklenemedi: "+string(out), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Uplink Ekleme", fmt.Sprintf("%s -> %s", iface, vswitch), "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Arayüz başarıyla " + vswitch + " köprüsüne bağlandı."})
}

func handleDetachUplink(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if iface == "" {
		http.Error(w, "Arayüz adı eksik", http.StatusBadRequest)
		return
	}

	// Detach from ANY bridge it might be on. We can find it by port name.
	cmd := exec.Command("ovs-vsctl", "del-port", iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface, "Başarısız")
		http.Error(w, "Arayüz çıkarılamadı: "+string(out), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Arayüz başarıyla koparıldı."})
}
