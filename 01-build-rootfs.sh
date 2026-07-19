#!/bin/bash
# 01-build-rootfs.sh
# Builds a minimal Ubuntu 24.04 (noble) rootfs via debootstrap that will be
# used BOTH as the live-boot environment AND as the final installed system
# (no separate subiquity/curtin installer stack -> much smaller image).
#
# Run as root on an Ubuntu 24.04 amd64 build host (or inside a matching
# container) with network access.

set -euo pipefail

ROOTFS_DIR="$(pwd)/rootfs"
RELEASE="noble"
MIRROR="http://archive.ubuntu.com/ubuntu"
TARGET_ADMIN_PASSWORD="${TARGET_ADMIN_PASSWORD:-admin}"

echo "====== [1/6] Installing build host dependencies ======"
apt-get update
apt-get install -y debootstrap squashfs-tools xorriso grub-pc-bin \
  grub-efi-amd64-bin grub-efi-amd64-signed shim-signed mtools golang-go git whois

echo "====== [2/6] Compiling BizBox application ======"
( cd bizbox-mvp && go build -buildvcs=false -o bizbox-mvp )

echo "====== [3/6] debootstrap: base system ======"
rm -rf "$ROOTFS_DIR"
debootstrap --arch=amd64 --variant=minbase "$RELEASE" "$ROOTFS_DIR" "$MIRROR"

# ---------------------------------------------------------------------------
# Prepare chroot (bind mounts)
# ---------------------------------------------------------------------------
mount --bind /dev "$ROOTFS_DIR/dev"
mount --bind /dev/pts "$ROOTFS_DIR/dev/pts"
mount -t proc proc "$ROOTFS_DIR/proc"
mount -t sysfs sysfs "$ROOTFS_DIR/sys"
cp /etc/resolv.conf "$ROOTFS_DIR/etc/resolv.conf"

cleanup() {
  umount -lf "$ROOTFS_DIR/dev/pts" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/dev" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/proc" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/sys" 2>/dev/null || true
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# APT sources: main + universe (zfsutils-linux, openvswitch, incus live in
# main/universe on 24.04)
# ---------------------------------------------------------------------------
cat > "$ROOTFS_DIR/etc/apt/sources.list" <<EOF
deb $MIRROR $RELEASE main universe
deb $MIRROR $RELEASE-updates main universe
deb $MIRROR $RELEASE-security main universe
EOF

echo "====== [4/6] Installing packages inside chroot ======"
chroot "$ROOTFS_DIR" /bin/bash -c "
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends \
    linux-image-generic \
    casper \
    udev \
    systemd-sysv \
    network-manager \
    sudo \
    zfsutils-linux \
    openvswitch-switch \
    incus incus-client \
    sqlite3 \
    iptables \
    curl \
    parted \
    gdisk \
    dosfstools \
    rsync \
    grub-pc-bin grub-efi-amd64-bin grub-efi-amd64-signed shim-signed
  apt-get clean
  rm -rf /var/lib/apt/lists/*
"

# ---------------------------------------------------------------------------
# Admin user + password
# ---------------------------------------------------------------------------
chroot "$ROOTFS_DIR" /bin/bash -c "
  useradd -m -s /bin/bash -G sudo admin || true
  echo 'admin:${TARGET_ADMIN_PASSWORD}' | chpasswd
  echo 'root:${TARGET_ADMIN_PASSWORD}' | chpasswd
  echo bizbox-host > /etc/hostname
"

echo "====== [5/6] Installing BizBox payload + service ======"
mkdir -p "$ROOTFS_DIR/opt/bizbox"
cp -r bizbox-mvp/bizbox-mvp bizbox-mvp/static bizbox-mvp/templates "$ROOTFS_DIR/opt/bizbox/"
chmod +x "$ROOTFS_DIR/opt/bizbox/bizbox-mvp"

cat > "$ROOTFS_DIR/etc/systemd/system/bizbox-mvp.service" <<'SYS'
[Unit]
Description=BizBox Hypervisor Manager
After=network.target incus.service openvswitch-switch.service
# Live boot sirasinda calismasin (sadece kurulu sistemde aktif olsun)
ConditionKernelCommandLine=!boot=casper

[Service]
Type=simple
User=root
WorkingDirectory=/opt/bizbox
ExecStart=/opt/bizbox/bizbox-mvp serve
Restart=always
Environment=AUTO_SNAPSHOT_INTERVAL_MINUTES=15

[Install]
WantedBy=multi-user.target
SYS

chroot "$ROOTFS_DIR" systemctl enable bizbox-mvp.service

echo "====== [6/6] Installing the on-boot installer service ======"
cp "$(dirname "$0")/bizbox-installer.sh" "$ROOTFS_DIR/usr/local/sbin/bizbox-installer.sh"
chmod +x "$ROOTFS_DIR/usr/local/sbin/bizbox-installer.sh"

cat > "$ROOTFS_DIR/etc/systemd/system/bizbox-installer.service" <<'SYS'
[Unit]
Description=BizBox first-boot installer (live media only)
DefaultDependencies=no
After=local-fs.target systemd-udevd.service systemd-udev-trigger.service systemd-udev-settle.service
Wants=systemd-udevd.service systemd-udev-trigger.service systemd-udev-settle.service
Before=getty@tty1.service
Conflicts=getty@tty1.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/bizbox-installer.sh
StandardInput=tty
StandardOutput=tty
TTYPath=/dev/tty1
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
SYS

# Enabled by default: the installer script itself detects (via /proc/cmdline
# boot=casper) whether it is actually running from live media, and is a
# no-op / self-disables otherwise, so it is safe that this stays enabled
# inside the copied target system too (see bizbox-installer.sh).
chroot "$ROOTFS_DIR" systemctl enable bizbox-installer.service

echo "====== Rootfs build complete: $ROOTFS_DIR ======"