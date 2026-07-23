#!/bin/bash
# 01-build-rootfs.sh
# Builds a minimal Ubuntu 24.04 (noble) rootfs via debootstrap that will be
# used BOTH as the live-boot environment AND as the final installed system.
#
# Run as root on an Ubuntu 24.04 amd64 build host with network access.
# Usage: sudo bash 01-build-rootfs.sh
#        TARGET_ADMIN_PASSWORD=mysecret sudo bash 01-build-rootfs.sh

set -euo pipefail

ROOTFS_DIR="$(pwd)/rootfs"
RELEASE="noble"
MIRROR="http://archive.ubuntu.com/ubuntu"
TARGET_ADMIN_PASSWORD="${TARGET_ADMIN_PASSWORD:-admin}"

# Script'in bulundugu dizini belirle (bizbox-installer.sh icin)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "====== [1/6] Installing build host dependencies ======"
apt-get update -qq
apt-get install -y \
  debootstrap squashfs-tools xorriso grub-pc-bin \
  grub-efi-amd64-bin grub-efi-amd64-signed shim-signed \
  mtools golang-go git

echo "====== [2/6] Compiling BizBox application ======"
( cd "$SCRIPT_DIR/bizbox-mvp" && go mod tidy && go build -buildvcs=false -o bizbox-mvp . )
echo "BizBox binary: OK"
ls -lh "$SCRIPT_DIR/bizbox-mvp/bizbox-mvp"

echo "====== [3/6] debootstrap: base system ======"
rm -rf "$ROOTFS_DIR"
debootstrap --arch=amd64 --variant=minbase "$RELEASE" "$ROOTFS_DIR" "$MIRROR"

# ---------------------------------------------------------------------------
# Prepare chroot (bind mounts)
# ---------------------------------------------------------------------------
mount --bind /dev     "$ROOTFS_DIR/dev"
mount --bind /dev/pts "$ROOTFS_DIR/dev/pts"
mount -t proc  proc   "$ROOTFS_DIR/proc"
mount -t sysfs sysfs  "$ROOTFS_DIR/sys"
cp /etc/resolv.conf "$ROOTFS_DIR/etc/resolv.conf"

cleanup() {
  echo "Cleaning up chroot mounts..."
  umount -lf "$ROOTFS_DIR/dev/pts" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/dev"     2>/dev/null || true
  umount -lf "$ROOTFS_DIR/proc"    2>/dev/null || true
  umount -lf "$ROOTFS_DIR/sys"     2>/dev/null || true
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# APT sources: main + universe
# ---------------------------------------------------------------------------
cat > "$ROOTFS_DIR/etc/apt/sources.list" <<EOF
deb $MIRROR $RELEASE           main restricted universe multiverse
deb $MIRROR $RELEASE-updates   main restricted universe multiverse
deb $MIRROR $RELEASE-security  main restricted universe multiverse
EOF

echo "====== [4/6] Installing packages inside chroot ======"
chroot "$ROOTFS_DIR" /bin/bash -c "
  set -e
  export DEBIAN_FRONTEND=noninteractive
  
  # Install curl and ca-certificates first to fetch the repo key
  apt-get update -qq
  apt-get install -y --no-install-recommends curl ca-certificates

  # Add Zabbly repository for Incus
  mkdir -p /etc/apt/keyrings/
  curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
  cat <<EOF > /etc/apt/sources.list.d/zabbly-incus-stable.sources
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: noble
Components: main
Architectures: amd64
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF

  apt-get update -qq
  apt-get install -y --no-install-recommends \
    linux-image-generic \
    linux-modules-extra-generic \
    linux-firmware \
    kmod \
    pciutils \
    lsscsi \
    nvme-cli \
    smartmontools \
    casper \
    udev \
    systemd systemd-sysv \
    dbus \
    network-manager \
    netplan.io \
    sudo \
    bash \
    zfsutils-linux \
    openvswitch-switch openvswitch-common \
    incus incus-client \
    sqlite3 \
    iptables nftables \
    curl wget \
    parted gdisk \
    dosfstools e2fsprogs \
    rsync \
    iproute2 \
    openssh-server \
    efibootmgr \
    systemd-resolved dnsmasq-base apparmor \
    grub-pc-bin grub-efi-amd64 grub-efi-amd64-signed shim-signed
  apt-get clean
  rm -rf /var/lib/apt/lists/*
"

# ---------------------------------------------------------------------------
# Admin kullanicisi + parola
# ---------------------------------------------------------------------------
chroot "$ROOTFS_DIR" /bin/bash -c "
  set -e
  useradd -m -s /bin/bash -G sudo admin 2>/dev/null || true
  echo 'admin:${TARGET_ADMIN_PASSWORD}' | chpasswd
  echo 'root:${TARGET_ADMIN_PASSWORD}' | chpasswd
  echo 'bizbox-host' > /etc/hostname
  echo '127.0.1.1 bizbox-host' >> /etc/hosts
"

# Sudo sifresiz (kurulum surecinde gerekli)
echo "admin ALL=(ALL) NOPASSWD:ALL" > "$ROOTFS_DIR/etc/sudoers.d/admin"
chmod 440 "$ROOTFS_DIR/etc/sudoers.d/admin"

# Sysctl configuration: Bridge netfilter disable for L2 isolation
mkdir -p "$ROOTFS_DIR/etc/sysctl.d"
cat > "$ROOTFS_DIR/etc/sysctl.d/99-bizbox-bridge.conf" <<'SYSCTL'
net.bridge.bridge-nf-call-iptables = 0
net.bridge.bridge-nf-call-ip6tables = 0
net.bridge.bridge-nf-call-arptables = 0
SYSCTL

# ---------------------------------------------------------------------------
# Netplan: tum ethernet arayzlerini DHCP ile otomatik yapilandir
# ---------------------------------------------------------------------------
mkdir -p "$ROOTFS_DIR/etc/netplan"
cat > "$ROOTFS_DIR/etc/netplan/01-bizbox-network.yaml" <<'NETPLAN'
network:
  version: 2
  renderer: NetworkManager
  ethernets:
    all-eth:
      match:
        name: "en*"
      dhcp4: true
      dhcp6: false
NETPLAN
chmod 600 "$ROOTFS_DIR/etc/netplan/01-bizbox-network.yaml"

# ---------------------------------------------------------------------------
# SSH: root ve admin login icin izin ver
# ---------------------------------------------------------------------------
mkdir -p "$ROOTFS_DIR/etc/ssh"
cat >> "$ROOTFS_DIR/etc/ssh/sshd_config" <<'SSHEOF'
PermitRootLogin yes
PasswordAuthentication yes
SSHEOF

# ---------------------------------------------------------------------------
# [5/6] BizBox payload + systemd service
# ---------------------------------------------------------------------------
echo "====== [5/6] Installing BizBox payload + service ======"
mkdir -p "$ROOTFS_DIR/opt/bizbox"
cp -r \
  "$SCRIPT_DIR/bizbox-mvp/bizbox-mvp" \
  "$SCRIPT_DIR/bizbox-mvp/static" \
  "$SCRIPT_DIR/bizbox-mvp/templates" \
  "$ROOTFS_DIR/opt/bizbox/"
chmod +x "$ROOTFS_DIR/opt/bizbox/bizbox-mvp"

cat > "$ROOTFS_DIR/etc/systemd/system/bizbox-mvp.service" <<'SYS'
[Unit]
Description=BizBox Hypervisor Manager
After=network-online.target incus.service openvswitch-switch.service
Wants=network-online.target
ConditionKernelCommandLine=!boot=casper

[Service]
Type=simple
User=root
WorkingDirectory=/opt/bizbox
ExecStart=/opt/bizbox/bizbox-mvp serve
Restart=on-failure
RestartSec=5
Environment=AUTO_SNAPSHOT_INTERVAL_MINUTES=15

[Install]
WantedBy=multi-user.target
SYS

chroot "$ROOTFS_DIR" systemctl enable bizbox-mvp.service
chroot "$ROOTFS_DIR" systemctl enable NetworkManager.service
chroot "$ROOTFS_DIR" systemctl enable ssh.service 2>/dev/null || \
  chroot "$ROOTFS_DIR" systemctl enable sshd.service 2>/dev/null || true

# Fix DNS via systemd-resolved
chroot "$ROOTFS_DIR" systemctl enable systemd-resolved.service
chroot "$ROOTFS_DIR" rm -f /etc/resolv.conf
chroot "$ROOTFS_DIR" ln -s ../run/systemd/resolve/stub-resolv.conf /etc/resolv.conf

# Create an initialization service for Incus so it runs automatically on first boot
cat > "$ROOTFS_DIR/etc/systemd/system/incus-init.service" <<'SYS'
[Unit]
Description=Initialize Incus default profile and pool
After=incus.service openvswitch-switch.service
Requires=incus.service openvswitch-switch.service
Before=bizbox-mvp.service
ConditionPathExists=!/var/lib/bizbox_incus_initialized
ConditionKernelCommandLine=!boot=casper

[Service]
Type=oneshot
ExecStart=/bin/bash -c "ovs-vsctl add-br br-int || true && incus admin init --auto && incus profile device add default root disk path=/ pool=default && touch /var/lib/bizbox_incus_initialized"
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
SYS

chroot "$ROOTFS_DIR" systemctl enable incus-init.service

# ---------------------------------------------------------------------------
# [6/6] Installer servisi (live boot'ta kurulumu gerceklestiren script)
# ---------------------------------------------------------------------------
echo "====== [6/6] Installing the on-boot installer service ======"
cp "$SCRIPT_DIR/bizbox-installer.sh" "$ROOTFS_DIR/usr/local/sbin/bizbox-installer.sh"
chmod +x "$ROOTFS_DIR/usr/local/sbin/bizbox-installer.sh"

cat > "$ROOTFS_DIR/etc/systemd/system/bizbox-installer.service" <<'SYS'
[Unit]
Description=BizBox first-boot installer (live media only)
DefaultDependencies=no
After=local-fs.target systemd-udevd.service systemd-udev-trigger.service systemd-udev-settle.service
Wants=systemd-udevd.service systemd-udev-trigger.service systemd-udev-settle.service
Before=getty@tty1.service
ConditionKernelCommandLine=boot=casper

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/bizbox-installer.sh
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/tty1
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
SYS

chroot "$ROOTFS_DIR" systemctl enable bizbox-installer.service

echo "====== Rootfs build complete: $ROOTFS_DIR ======"
echo "Simdi 02-build-live-iso.sh calistirin."