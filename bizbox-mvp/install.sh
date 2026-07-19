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

# 2. Update Packages and Install Dependencies
echo "Installing system packages..."
apt-get update
apt-get install -y \
  incus \
  incus-client \
  openvswitch-switch \
  zfsutils-linux \
  golang-go \
  sqlite3 \
  tc \
  iptables \
  git \
  curl

# 3. Configure ZFS Storage Pool for Incus/BizBox
echo "Configuring ZFS storage pool..."
if ! zpool list | grep -q "rft"; then
  # Create a loopback disk for ZFS if no raw block device is provided
  ZFS_IMG="/var/lib/bizbox_zfs.img"
  if [ ! -f "$ZFS_IMG" ]; then
    echo "Creating loopback image for ZFS pool..."
    truncate -s 20G "$ZFS_IMG"
  fi
  zpool create rft "$ZFS_IMG"
  echo "ZFS pool 'rft' created successfully."
else
  echo "ZFS pool 'rft' already exists."
fi

# Ensure datasets exist
zfs create -p rft/virtual-machines || true
zfs create -p rft/containers || true

# 4. Configure Integration Bridge (OVS)
echo "Configuring Open vSwitch..."
if ! ovs-vsctl show | grep -q "br-int"; then
  ovs-vsctl add-br br-int
  echo "OVS integration bridge 'br-int' created."
fi

# 5. Build BizBox Binary
echo "Building BizBox Go application..."
go build -o bizbox-mvp

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
