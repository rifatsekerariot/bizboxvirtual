package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
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
	Segments  []Segment     `json:"segments"`
}

func handleGetUplinks(w http.ResponseWriter, r *http.Request) {
	c := GetIncusClient()
	if c == nil {
		http.Error(w, "Incus client connection failed", http.StatusInternalServerError)
		return
	}

	// 1. Get physical interfaces via Go's net package
	ifaces, err := net.Interfaces()
	if err != nil {
		http.Error(w, "Could not read network interfaces", http.StatusInternalServerError)
		return
	}

	validPhysicalLinks := make(map[string]bool)
	var uplinks []UplinkInfo
	for _, iface := range ifaces {
		name := iface.Name
		// Filter out virtual, loopback, or bridge interfaces
		if (iface.Flags&net.FlagLoopback != 0) || strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "vSwitch") ||
			name == "ovs-system" || strings.HasPrefix(name, "incus") || strings.HasPrefix(name, "lo") {
			continue
		}

		validPhysicalLinks[name] = true
		isUp := (iface.Flags & net.FlagUp) != 0

		uplinks = append(uplinks, UplinkInfo{
			Name:       name,
			IsUp:       isUp,
			MacAddress: iface.HardwareAddr.String(),
			VSwitch:    "",
		})
	}

	// 2. Get Incus managed networks (bridges)
	networks, err := c.GetNetworks()
	if err != nil {
		http.Error(w, "Failed to get Incus networks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var vswitches []VSwitchInfo
	for _, netObj := range networks {
		if netObj.Type != "bridge" {
			continue
		}

		// A standard Incus bridge can have external_interfaces defined in its config
		extIfacesRaw := netObj.Config["bridge.external_interfaces"]
		var attachedPhys []string
		if extIfacesRaw != "" {
			parts := strings.Split(extIfacesRaw, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					attachedPhys = append(attachedPhys, p)
					// Mark the physical uplink as attached to this vswitch
					for i := range uplinks {
						if uplinks[i].Name == p {
							uplinks[i].VSwitch = netObj.Name
							break
						}
					}
				}
			}
		}

		vswitches = append(vswitches, VSwitchInfo{
			Name:       netObj.Name,
			IsDefault:  netObj.Name == "incusbr0",
			Interfaces: attachedPhys,
		})
	}

	response := NetworkUplinkResponse{
		VSwitches: vswitches,
		Uplinks:   uplinks,
		Segments:  ListNetworkSegments(),
	}

	// Since we are rendering via HTMX to the uplinks.html template, we pass the data to the template
	err = templates.ExecuteTemplate(w, "uplinks.html", response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleCreateVSwitch(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "vSwitch name missing", http.StatusBadRequest)
		return
	}

	c := GetIncusClient()
	if c == nil {
		http.Error(w, "Incus client error", http.StatusInternalServerError)
		return
	}

	req := api.NetworksPost{
		Name: name,
		Type: "bridge",
		NetworkPut: api.NetworkPut{
			Description: "Managed vSwitch (Uplink)",
			Config: map[string]string{
				"ipv4.address": "none",
				"ipv6.address": "none",
				"bridge.driver": "openvswitch", // Optional, if OVS is installed, it gives better VLAN support
			},
		},
	}

	err := c.CreateNetwork(req)
	if err != nil {
		LogSystemEvent(getUsername(r), "vSwitch Oluşturma", name, "Başarısız: "+err.Error())
		http.Error(w, "Could not create vSwitch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "vSwitch Oluşturma", name, "Başarılı")
	w.WriteHeader(http.StatusOK)
}

func handleDeleteVSwitch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name == "incusbr0" {
		http.Error(w, "Geçersiz veya silinemez vSwitch", http.StatusBadRequest)
		return
	}

	c := GetIncusClient()
	if c == nil {
		http.Error(w, "Incus client error", http.StatusInternalServerError)
		return
	}

	err := c.DeleteNetwork(name)
	if err != nil {
		LogSystemEvent(getUsername(r), "vSwitch Silme", name, "Başarısız: "+err.Error())
		http.Error(w, "Could not delete vSwitch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "vSwitch Silme", name, "Başarılı")
	w.WriteHeader(http.StatusOK)
}

func handleAttachUplink(w http.ResponseWriter, r *http.Request) {
	// For form submits from HTMX
	iface := r.FormValue("iface")
	vswitch := r.FormValue("vswitch")
	if iface == "" || vswitch == "" {
		http.Error(w, "Arayüz veya vSwitch adı eksik", http.StatusBadRequest)
		return
	}

	c := GetIncusClient()
	if c == nil {
		http.Error(w, "Incus client error", http.StatusInternalServerError)
		return
	}

	netObj, _, err := c.GetNetwork(vswitch)
	if err != nil {
		http.Error(w, "Network not found", http.StatusNotFound)
		return
	}

	// Add interface to bridge.external_interfaces
	extIfaces := netObj.Config["bridge.external_interfaces"]
	if extIfaces == "" {
		extIfaces = iface
	} else {
		// Check if it's already there
		parts := strings.Split(extIfaces, ",")
		for _, p := range parts {
			if strings.TrimSpace(p) == iface {
				w.WriteHeader(http.StatusOK)
				return // Already attached
			}
		}
		extIfaces += "," + iface
	}
	netObj.Config["bridge.external_interfaces"] = extIfaces

	err = c.UpdateNetwork(vswitch, netObj.NetworkPut, "")
	if err != nil {
		LogSystemEvent(getUsername(r), "Uplink Ekleme", fmt.Sprintf("%s -> %s", iface, vswitch), "Başarısız: "+err.Error())
		http.Error(w, "Failed to attach: "+err.Error(), http.StatusInternalServerError)
		return
	}

	LogSystemEvent(getUsername(r), "Uplink Ekleme", fmt.Sprintf("%s -> %s", iface, vswitch), "Başarılı")
	w.WriteHeader(http.StatusOK)
}

func handleDetachUplink(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if iface == "" {
		http.Error(w, "Arayüz adı eksik", http.StatusBadRequest)
		return
	}

	c := GetIncusClient()
	if c == nil {
		http.Error(w, "Incus client error", http.StatusInternalServerError)
		return
	}

	// We need to find which network this interface is attached to
	networks, err := c.GetNetworks()
	if err != nil {
		http.Error(w, "Could not list networks", http.StatusInternalServerError)
		return
	}

	for _, netObj := range networks {
		if netObj.Type != "bridge" {
			continue
		}
		extIfaces := netObj.Config["bridge.external_interfaces"]
		if extIfaces == "" {
			continue
		}
		
		parts := strings.Split(extIfaces, ",")
		var newParts []string
		found := false
		for _, p := range parts {
			if strings.TrimSpace(p) == iface {
				found = true
			} else {
				newParts = append(newParts, strings.TrimSpace(p))
			}
		}

		if found {
			netObj.Config["bridge.external_interfaces"] = strings.Join(newParts, ",")
			err = c.UpdateNetwork(netObj.Name, netObj.NetworkPut, "")
			if err != nil {
				LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface, "Başarısız: "+err.Error())
				http.Error(w, "Failed to detach: "+err.Error(), http.StatusInternalServerError)
				return
			}
			LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface, "Başarılı")
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// If we got here, interface wasn't found on any network
	w.WriteHeader(http.StatusOK)
}
