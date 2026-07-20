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
# Artık kurulumda otomatik 'rft' pool oluşturmuyoruz. Kullanıcı bunu yönetim panelindeki 'Depolama' sekmesinden yapacaktır.
echo "ZFS Storage pool yönetimi arayüze bırakıldı."


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

# Reload and enable service
systemctl daemon-reload
systemctl enable bizbox-mvp.service
systemctl restart bizbox-mvp.service

echo "====== Setup Completed Successfully! ======"
echo "You can access the panel at http://<your-server-ip>:8080"
echo "Default Credentials: admin / admin"
