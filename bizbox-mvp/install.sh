#!/bin/bash
set -e

# BizBox Automated Installer for Debian/Ubuntu Systems
# Run this script as root: sudo ./install.sh

echo "====== BizBox Automated Setup starting ======"

# 1. Check Root
if [ "$EUID" -ne 0 ]; then
  echo "Error: Please run this installer as root."
  exit 1
fi

# 1.1 Interactive Root Password Setup
echo ""
echo "========================================================"
echo "          Root Parolası Yapılandırması                  "
echo "========================================================"
while true; do
  read -s -p "Lütfen bu sunucu için ROOT kullanıcısı parolası belirleyin: " ROOT_PASS
  echo ""
  read -s -p "Parolayı tekrar girin: " ROOT_PASS_CONFIRM
  echo ""
  if [ -n "$ROOT_PASS" ] && [ "$ROOT_PASS" = "$ROOT_PASS_CONFIRM" ]; then
    echo "root:$ROOT_PASS" | chpasswd
    echo "Root parolası başarıyla ayarlandı."
    break
  else
    echo "Hata: Parolalar uyuşmuyor veya boş bırakıldı. Lütfen tekrar deneyin."
  fi
done

# 1.2 Interactive Static IP & Management NIC Setup
echo ""
echo "========================================================"
echo "      Hipervizör Statik Ağ (IP) Yapılandırması        "
echo "========================================================"

ETH_INTERFACES=$(ip -o link show | awk -F': ' '{print $2}' | grep -v -E '^(lo|virbr|br-|docker|veth|tap|tun|ovs)')
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)

if [ -z "$DEFAULT_IFACE" ]; then
  DEFAULT_IFACE=$(echo "$ETH_INTERFACES" | head -n1)
fi

echo "Tespit edilen fiziksel ağ kartları:"
echo "$ETH_INTERFACES"
echo ""
read -p "Yönetim (Management) ağ kartı seçin [Varsayılan: $DEFAULT_IFACE]: " SELECTED_IFACE
SELECTED_IFACE=${SELECTED_IFACE:-$DEFAULT_IFACE}

echo ""
echo "Ağ Yapılandırma Tipi:"
echo " 1) Statik IP Adresi (Önerilen - Hipervizör için Sabit IP)"
echo " 2) DHCP (Dinamik IP)"
read -p "Seçiminiz (1 veya 2) [Varsayılan: 1]: " NET_CHOICE
NET_CHOICE=${NET_CHOICE:-1}

if [ "$NET_CHOICE" = "1" ]; then
  CURR_IP=$(ip -4 addr show dev $SELECTED_IFACE 2>/dev/null | grep inet | awk '{print $2}' | cut -d/ -f1 | head -n1)
  CURR_GW=$(ip route | grep default | awk '{print $3}' | head -n1)
  
  read -p "Statik IP Adresi [Ör: 192.168.1.100] ${CURR_IP:+(Mevcut: $CURR_IP)}: " STATIC_IP
  STATIC_IP=${STATIC_IP:-$CURR_IP}
  
  read -p "CIDR Alt Ağ Prefiksi (örn: 24 = 255.255.255.0) [Varsayılan: 24]: " PREFIX
  PREFIX=${PREFIX:-24}
  if [ "$PREFIX" = "255.255.255.0" ]; then PREFIX="24"; fi
  
  read -p "Varsayılan Ağ Geçidi (Gateway) ${CURR_GW:+(Mevcut: $CURR_GW)}: " GATEWAY_IP
  GATEWAY_IP=${GATEWAY_IP:-$CURR_GW}
  
  read -p "DNS Sunucuları [Varsayılan: 8.8.8.8, 1.1.1.1]: " DNS_SERVERS
  DNS_SERVERS=${DNS_SERVERS:-"8.8.8.8, 1.1.1.1"}

  mkdir -p /etc/netplan
  rm -f /etc/netplan/*bizbox*.yaml 2>/dev/null || true
  
  cat <<NETPLAN > /etc/netplan/01-bizbox-static.yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    ${SELECTED_IFACE}:
      dhcp4: false
      dhcp6: false
      addresses:
        - ${STATIC_IP}/${PREFIX}
      routes:
        - to: default
          via: ${GATEWAY_IP}
      nameservers:
        addresses: [${DNS_SERVERS}]
NETPLAN
  chmod 600 /etc/netplan/01-bizbox-static.yaml
  echo "Netplan statik IP yapılandırması uygulanıyor..."
  netplan apply || true
  echo "Hipervizör Statik IP adresi sabitlendi: ${STATIC_IP}/${PREFIX}"
else
  echo "DHCP ağ yapılandırması seçildi."
fi

# 2. Add Zabbly repository for Incus (Fixes OVS JSON-RPC bugs)
echo "Adding Zabbly repository for Incus..."
mkdir -p /etc/apt/keyrings/
curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
sh -c 'cat <<EOF > /etc/apt/sources.list.d/zabbly-incus-stable.sources
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: noble
Components: main
Architectures: amd64
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF'

# 3. Update Packages and Install Dependencies
echo "Installing system packages..."
apt-get update
apt-get install -y \
  incus \
  incus-client \
  openvswitch-switch \
  zfsutils-linux \
  sqlite3 \
  iproute2 \
  iptables \
  git \
  curl

# 3. Datastore (ZFS Pool) Yönetimi
echo "ZFS Storage pool yönetimi arayüze bırakıldı."

# 3.1 Configure Sysctl for L2 Bridge Isolation (Prevent host iptables interference on VM-VM traffic)
echo "Configuring Sysctl bridge-nf parameters..."
cat <<EOF > /etc/sysctl.d/99-bizbox-bridge.conf
net.bridge.bridge-nf-call-iptables = 0
net.bridge.bridge-nf-call-ip6tables = 0
net.bridge.bridge-nf-call-arptables = 0
EOF
sysctl -p /etc/sysctl.d/99-bizbox-bridge.conf || true

# 4. Configure Integration Bridge (OVS)
echo "Configuring Open vSwitch..."
if ! ovs-vsctl show | grep -q "br-int"; then
  ovs-vsctl add-br br-int
  echo "OVS integration bridge 'br-int' created."
fi

# 5. Check BizBox Binary
echo "Checking pre-compiled BizBox Go application..."
if [ ! -f "bizbox-mvp" ]; then
  echo "Error: Pre-compiled 'bizbox-mvp' binary not found in this directory."
  exit 1
fi
chmod +x bizbox-mvp

# 6. Configure Systemd Service
echo "Configuring Systemd service..."
cat <<EOF > /etc/systemd/system/bizbox-mvp.service
[Unit]
Description=BizBox Hypervisor Manager
After=network.target incus.service openvswitch-switch.service

[Service]
Type=simple
User=root
WorkingDirectory=$(pwd)
ExecStart=$(pwd)/bizbox-mvp serve
Restart=always
Environment=AUTO_SNAPSHOT_INTERVAL_MINUTES=15

[Install]
WantedBy=multi-user.target
EOF

# 7. Configure Persistent TTY Console Banner (ESXi / Proxmox Style)
echo "Configuring persistent TTY Console Banner..."
cat <<'EOF' > /usr/local/bin/bizbox-refresh-banner.sh
#!/bin/bash
IPS=$(ip -4 addr show scope global | grep inet | awk '{print $2}' | cut -d/ -f1 | tr '\n' ' ')
HOSTNAME=$(hostname)
FIRST_IP=$(echo $IPS | awk '{print $1}')

if [ -z "$FIRST_IP" ]; then
  FIRST_IP="<ip-adresi-bekleniyor>"
fi

cat <<BANNER > /etc/issue
====================================================================
               BIZBOX ENTERPRISE HYPERVISOR APPLIANCE               
====================================================================
  Kurulan Sistem  : BizBox Virtualization Appliance v1.0.0
  Sunucu İsmi     : ${HOSTNAME}
  Ağ IP Adresleri : ${IPS}

  Yönetim Arayüzü : https://${FIRST_IP}:8443  (HTTPS - Güvenli)
                  : http://${FIRST_IP}:8080   (HTTP Yönlendirme)

  Web Panel Girişi : Kullanıcı: admin / Şifre: admin (Değiştiriniz)
  Root Konsol/SSH  : Kurulumda belirlediğiniz ROOT şifresi

  Dokümantasyon   : https://github.com/rifatsekerariot/bizboxvirtual
====================================================================

BANNER
cp /etc/issue /etc/issue.net
EOF

chmod +x /usr/local/bin/bizbox-refresh-banner.sh

cat <<EOF > /etc/systemd/system/bizbox-banner.service
[Unit]
Description=BizBox Dynamic TTY Console Banner
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/bizbox-refresh-banner.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

# Reload and enable services
systemctl daemon-reload
systemctl enable bizbox-mvp.service
systemctl enable bizbox-banner.service
systemctl restart bizbox-mvp.service
systemctl restart bizbox-banner.service

# Initial banner update
/usr/local/bin/bizbox-refresh-banner.sh || true

echo ""
cat /etc/issue
echo "====== BizBox Kurulumu Başarıyla Tamamlandı! ======"
