package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
)

type UplinkInfo struct {
	Name       string `json:"name"`
	IsUp       bool   `json:"is_up"`
	MacAddress string `json:"mac_address"`
	IsAttached bool   `json:"is_attached"`
}

// GET /api/network/uplinks
func handleGetUplinks(w http.ResponseWriter, r *http.Request) {
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

	cmdOVS := exec.Command("ovs-vsctl", "list-ports", "br-int")
	ovsOut, _ := cmdOVS.Output()
	attachedPorts := strings.Split(strings.TrimSpace(string(ovsOut)), "\n")

	var uplinks []UplinkInfo
	for _, l := range links {
		// Ignore loopback, ovs bridges, veth pairs, incus bridges
		if l.LinkType == "loopback" || strings.HasPrefix(l.Ifname, "veth") || strings.HasPrefix(l.Ifname, "br-") || l.Ifname == "ovs-system" || strings.HasPrefix(l.Ifname, "incus") {
			continue
		}

		isAttached := false
		for _, p := range attachedPorts {
			if p == l.Ifname {
				isAttached = true
				break
			}
		}

		uplinks = append(uplinks, UplinkInfo{
			Name:       l.Ifname,
			IsUp:       l.Operstate == "UP" || l.Operstate == "UNKNOWN",
			MacAddress: l.Address,
			IsAttached: isAttached,
		})
	}
	if uplinks == nil {
		uplinks = []UplinkInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(uplinks)
}

// POST /api/network/uplinks/{iface}/attach
func handleAttachUplink(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if iface == "" {
		http.Error(w, "Arayüz adı eksik", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("ovs-vsctl", "add-port", "br-int", iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "Uplink Ekleme", iface, "Başarısız")
		http.Error(w, "Arayüz eklenemedi: "+string(out), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Uplink Ekleme", iface, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Arayüz başarıyla br-int'e bağlandı."})
}

// POST /api/network/uplinks/{iface}/detach
func handleDetachUplink(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if iface == "" {
		http.Error(w, "Arayüz adı eksik", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("ovs-vsctl", "del-port", "br-int", iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface, "Başarısız")
		http.Error(w, "Arayüz çıkarılamadı: "+string(out), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface, "Başarılı")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Arayüz başarıyla koparıldı."})
}
