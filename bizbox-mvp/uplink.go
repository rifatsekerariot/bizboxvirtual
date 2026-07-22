package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
)

type VSwitchInfo struct {
	Name       string   `json:"name"`
	IsDefault  bool     `json:"is_default"`
	Interfaces []string `json:"interfaces"`
}

type UplinkInfo struct {
	Name         string `json:"name"`
	IsUp         bool   `json:"is_up"`
	MacAddress   string `json:"mac_address"`
	IPAddress    string `json:"ip_address"`
	Netmask      string `json:"netmask"`
	Gateway      string `json:"gateway"`
	VSwitch      string `json:"vswitch"`
	IsManagement bool   `json:"is_management"`
}

type NetworkUplinkResponse struct {
	VSwitches     []VSwitchInfo `json:"vswitches"`
	Uplinks       []UplinkInfo  `json:"uplinks"`
	Segments      []Segment     `json:"segments"`
	ManagementIf  string        `json:"management_if"`
	ManagementIP  string        `json:"management_ip"`
	DefaultGateway string       `json:"default_gateway"`
}

func getInterfaceIPAndMask(ifaceName string) (ipAddr string, netmask string) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return "", ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ipAddr = ipNet.IP.String()
				mask := net.IP(ipNet.Mask)
				netmask = fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
				return ipAddr, netmask
			}
		}
	}
	return "", ""
}

func getDefaultRouteInterface() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			// Destination is the second column (00000000 for default route)
			if fields[1] == "00000000" {
				return fields[0] // Interface name
			}
		}
	}
	return ""
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

	mgmtIface := getDefaultRouteInterface()

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
		ip, mask := getInterfaceIPAndMask(name)

		uplinks = append(uplinks, UplinkInfo{
			Name:         name,
			IsUp:         isUp,
			MacAddress:   iface.HardwareAddr.String(),
			IPAddress:    ip,
			Netmask:      mask,
			VSwitch:      "",
			IsManagement: name == mgmtIface,
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

		var attachedPhys []string

		// Check if it's an OVS bridge
		cmdOvsCheck := exec.Command("ovs-vsctl", "br-exists", netObj.Name)
		if errOvs := cmdOvsCheck.Run(); errOvs == nil {
			// Get interfaces from OVS
			cmdOvsList := exec.Command("ovs-vsctl", "list-ifaces", netObj.Name)
			if out, errOvsList := cmdOvsList.Output(); errOvsList == nil {
				lines := strings.Split(string(out), "\n")
				for _, line := range lines {
					p := strings.TrimSpace(line)
					// Exclude internal ports (same name as bridge) and virtual VM ports
					if p != "" && p != netObj.Name && !strings.HasPrefix(p, "veth") && !strings.HasPrefix(p, "tap") {
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
		} else {
			// A standard Incus bridge can have external_interfaces defined in its config
			extIfacesRaw := netObj.Config["bridge.external_interfaces"]
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
		}

		vswitches = append(vswitches, VSwitchInfo{
			Name:       netObj.Name,
			IsDefault:  netObj.Name == "incusbr0",
			Interfaces: attachedPhys,
		})
	}

	mgmtIP, _ := getInterfaceIPAndMask(mgmtIface)

	response := NetworkUplinkResponse{
		VSwitches:     vswitches,
		Uplinks:       uplinks,
		Segments:      ListNetworkSegments(),
		ManagementIf:  mgmtIface,
		ManagementIP:  mgmtIP,
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

	// If it exists in OVS, delete it via OVS first
	cmdCheck := exec.Command("ovs-vsctl", "br-exists", name)
	if errOvs := cmdCheck.Run(); errOvs == nil {
		cmdDel := exec.Command("ovs-vsctl", "del-br", name)
		_ = cmdDel.Run()
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

	// Prevent attaching the active management interface
	if iface == getDefaultRouteInterface() {
		http.Error(w, "Yönetim arayüzü ("+iface+") bir sanal anahtara doğrudan bağlanamaz. Bu işlem sunucu erişimini kesecektir!", http.StatusBadRequest)
		return
	}

	// 1. Check if the bridge exists in OVS
	cmdCheck := exec.Command("ovs-vsctl", "br-exists", vswitch)
	if errOvs := cmdCheck.Run(); errOvs == nil {
		// It is an OVS bridge, attach via OVS add-port
		cmdAttach := exec.Command("ovs-vsctl", "add-port", vswitch, iface)
		if out, errAttach := cmdAttach.CombinedOutput(); errAttach != nil {
			LogSystemEvent(getUsername(r), "Uplink Ekleme", fmt.Sprintf("%s -> %s (OVS)", iface, vswitch), "Başarısız: "+errAttach.Error())
			http.Error(w, "Failed to attach interface via OVS: "+string(out), http.StatusInternalServerError)
			return
		}
		LogSystemEvent(getUsername(r), "Uplink Ekleme", fmt.Sprintf("%s -> %s (OVS)", iface, vswitch), "Başarılı")
		w.WriteHeader(http.StatusOK)
		return
	}

	// 2. Fallback to standard Incus managed bridge
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

	// 1. Check if the port is attached to an OVS bridge
	cmdCheck := exec.Command("ovs-vsctl", "port-to-br", iface)
	if out, errOvs := cmdCheck.CombinedOutput(); errOvs == nil {
		// It is an OVS port! Delete it from OVS directly
		cmdDetach := exec.Command("ovs-vsctl", "del-port", iface)
		if errDetach := cmdDetach.Run(); errDetach != nil {
			LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface+" (OVS)", "Başarısız: "+errDetach.Error())
			http.Error(w, "Failed to detach interface via OVS: "+string(out), http.StatusInternalServerError)
			return
		}
		LogSystemEvent(getUsername(r), "Uplink Çıkarma", iface+" (OVS)", "Başarılı")
		w.WriteHeader(http.StatusOK)
		return
	}

	// 2. Fallback to Incus client for standard bridges
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

func handleRenewDHCP(w http.ResponseWriter, r *http.Request) {
	iface := r.FormValue("iface")
	if iface == "" {
		iface = r.PathValue("iface")
	}
	if iface == "" {
		http.Error(w, "Arayüz adı eksik", http.StatusBadRequest)
		return
	}

	// 1. Send dhclient command in background
	cmdDhclient := exec.Command("dhclient", "-r", iface)
	_ = cmdDhclient.Run()
	cmdDhclientRenew := exec.Command("dhclient", iface)
	err := cmdDhclientRenew.Run()

	if err != nil {
		// Fallback to netplan / ip link up
		_ = exec.Command("netplan", "apply").Run()
		_ = exec.Command("ip", "link", "set", iface, "up").Run()
	}

	LogSystemEvent(getUsername(r), "DHCP IP Yenileme", iface, "DHCP Yenileme İsteği Gönderildi")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("DHCP IP yenileme isteği başarıyla gönderildi."))
}

func handleConfigureManagementIP(w http.ResponseWriter, r *http.Request) {
	iface := r.FormValue("iface")
	mode := r.FormValue("mode") // "dhcp" or "static"
	ipAddr := r.FormValue("ip")
	netmask := r.FormValue("netmask")
	gateway := r.FormValue("gateway")

	if iface == "" {
		http.Error(w, "Arayüz adı eksik", http.StatusBadRequest)
		return
	}

	if mode == "dhcp" {
		cmd := exec.Command("dhclient", iface)
		_ = cmd.Run()
		_ = exec.Command("netplan", "apply").Run()
		LogSystemEvent(getUsername(r), "Yönetim IP Yapılandırma", fmt.Sprintf("%s -> DHCP", iface), "Başarılı")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Arayüz DHCP moduna alındı."))
		return
	}

	if mode == "static" {
		if ipAddr == "" {
			http.Error(w, "IP adresi zorunludur", http.StatusBadRequest)
			return
		}
		prefix := "24"
		if netmask != "" {
			maskIP := net.ParseIP(netmask)
			if maskIP != nil {
				mask := net.IPMask(maskIP.To4())
				ones, _ := mask.Size()
				if ones > 0 {
					prefix = fmt.Sprintf("%d", ones)
				}
			}
		}

		// Flush old IP & set new static IP
		_ = exec.Command("ip", "addr", "flush", "dev", iface).Run()
		cmdAdd := exec.Command("ip", "addr", "add", fmt.Sprintf("%s/%s", ipAddr, prefix), "dev", iface)
		if out, err := cmdAdd.CombinedOutput(); err != nil {
			http.Error(w, "IP adresi eklenirken hata: "+string(out), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("ip", "link", "set", iface, "up").Run()

		if gateway != "" {
			_ = exec.Command("ip", "route", "add", "default", "via", gateway, "dev", iface).Run()
		}

		LogSystemEvent(getUsername(r), "Yönetim IP Yapılandırma", fmt.Sprintf("%s -> %s/%s", iface, ipAddr, prefix), "Başarılı")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Statik IP adresi başarıyla uygulandı."))
		return
	}

	http.Error(w, "Geçersiz mod", http.StatusBadRequest)
}

