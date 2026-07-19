#!/bin/bash
# BizBox Automated ISO Builder - Offline Package Insertion Version (FIXED)
# Run this on your Linux server as root. Recommended: run on an Ubuntu 24.04
# (noble) host, or inside an `ubuntu:24.04` container, so downloaded .deb
# packages are guaranteed ABI/version-compatible with the target install.

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
LATEST_ISO_FILE=$(curl -s https://releases.ubuntu.com/24.04/ | grep -oE 'ubuntu-24\.04\.[0-9]+-live-server-amd64\.iso' | sort -V | tail -n 1)
if [ -z "$LATEST_ISO_FILE" ]; then
  LATEST_ISO_FILE="ubuntu-24.04.1-live-server-amd64.iso"
fi
UBUNTU_ISO_URL="https://releases.ubuntu.com/24.04/$LATEST_ISO_FILE"
ORIGINAL_ISO="$LATEST_ISO_FILE"
CUSTOM_ISO="bizbox-installer.iso"
TEMP_DIR="./iso-temp"

# Default target OS credentials. CHANGE THIS before building, or better,
# pass it in via env var: TARGET_ADMIN_PASSWORD=... ./build-bizbox-iso.sh
TARGET_ADMIN_PASSWORD="${TARGET_ADMIN_PASSWORD:-admin}"

echo "====== Starting Offline Packages BizBox ISO Build ======"

# ---------------------------------------------------------------------------
# 1. Install required packages on build host
#    (dpkg-dev added: needed for apt-ftparchive to build a local repo index)
# ---------------------------------------------------------------------------
echo "Installing build dependencies..."
apt-get update
apt-get install -y xorriso squashfs-tools wget curl golang-go git dpkg-dev whois

# ---------------------------------------------------------------------------
# 2. Compile BizBox Go Application
# ---------------------------------------------------------------------------
echo "Compiling BizBox application..."
( cd bizbox-mvp && go build -buildvcs=false -o bizbox-mvp )

# ---------------------------------------------------------------------------
# 3. Download Ubuntu Base ISO if not present
# ---------------------------------------------------------------------------
if [ ! -f "$ORIGINAL_ISO" ]; then
  echo "Downloading base Ubuntu Server ISO ($ORIGINAL_ISO)..."
  wget -O "$ORIGINAL_ISO" "$UBUNTU_ISO_URL"
fi

# ---------------------------------------------------------------------------
# 4. Prepare temporary directory structure
# ---------------------------------------------------------------------------
echo "Preparing temporary directories..."
rm -rf "$TEMP_DIR"
mkdir -p "$TEMP_DIR/nocloud"
mkdir -p "$TEMP_DIR/payload"
mkdir -p "$TEMP_DIR/pool"

# ---------------------------------------------------------------------------
# 5. Download all offline .deb packages and dependencies, then build a real
#    local APT repository out of them (instead of relying on manual
#    `dpkg -i` ordering, which breaks when the dependency graph isn't a
#    perfect topological match).
# ---------------------------------------------------------------------------
echo "Downloading offline package dependencies..."
(
  cd "$TEMP_DIR/pool"
  apt-get download $(apt-cache depends --recurse --no-recommends --no-suggests \
      --no-conflicts --no-breaks --no-replaces --no-enhances \
      openvswitch-switch zfsutils-linux incus incus-client sqlite3 iptables curl \
      | grep "^\w" | sort -u) || true
)

echo "Building local APT repository index for offline install..."
(
  cd "$TEMP_DIR"
  apt-ftparchive packages pool > pool/Packages
  gzip -k9f pool/Packages
  apt-ftparchive release pool > pool/Release
)

# ---------------------------------------------------------------------------
# 6. Copy BizBox files into payload
# ---------------------------------------------------------------------------
echo "Preparing BizBox payload..."
cp -r bizbox-mvp/bizbox-mvp bizbox-mvp/static bizbox-mvp/templates "$TEMP_DIR/payload/"

# ---------------------------------------------------------------------------
# 7. Create Autoinstall configuration (user-data & meta-data)
# ---------------------------------------------------------------------------
echo "Creating Autoinstall configurations..."
touch "$TEMP_DIR/nocloud/meta-data"

# Generate a proper SHA-512 crypt hash for the chosen password so we don't
# need to hardcode a stale/mismatched hash.
ADMIN_HASH=$(mkpasswd -m sha-512 "$TARGET_ADMIN_PASSWORD" 2>/dev/null || openssl passwd -6 "$TARGET_ADMIN_PASSWORD")

cat <<EOF > "$TEMP_DIR/nocloud/user-data"
#cloud-config
autoinstall:
  version: 1
  identity:
    hostname: bizbox-host
    password: "$ADMIN_HASH"
    username: admin
  locale: en_US.UTF-8
  keyboard:
    layout: us
  storage:
    layout:
      name: direct
      # Deterministic disk pick so the installer NEVER has to ask which disk
      # to use when multiple disks are present (fixes silent hang/prompt).
      match:
        size: largest
  user-data:
    disable_root: false
  early-commands:
    - |
      clear > /dev/tty1
      echo -e "\n\n\n\n\n\n" > /dev/tty1
      echo -e "\t############################################################" > /dev/tty1
      echo -e "\t#                                                          #" > /dev/tty1
      echo -e "\t#                BIZBOX HYPERVISOR INSTALLER               #" > /dev/tty1
      echo -e "\t#                        by ARIOT                          #" > /dev/tty1
      echo -e "\t#                                                          #" > /dev/tty1
      echo -e "\t############################################################" > /dev/tty1
      echo -e "\n" > /dev/tty1
      echo -e "\t    Sanal hypervisor sistemi kuruluyor, lutfen bekleyin... " > /dev/tty1
      echo -e "\t    [Bu islem birkac dakika surebilir]" > /dev/tty1
      echo -e "\n\t    Kurulum durumu: DISKLER TEMIZLENIYOR (WIPING DISKS)..." > /dev/tty1

      # Wipe all physical disks except the installation source itself.
      # Check multiple possible mount points for the install medium
      # (casper can mount it at /cdrom, /media, or /run/live/medium).
      for disk in \$(lsblk -dn -o NAME,TYPE | grep -E "disk" | awk '{print \$1}'); do
        if mount | grep -E "^/dev/\${disk}[0-9]*" | grep -q -E "/cdrom|/media|/run/live|/rofs"; then
          continue
        fi
        wipefs -a -f "/dev/\$disk" || true
        dd if=/dev/zero of="/dev/\$disk" bs=1M count=50 conv=fdatasync || true
      done

      echo -e "\n\t    Kurulum durumu: DISKLER BASARIYLA TEMIZLENDI. DOSYALAR KOPYALANIYOR..." > /dev/tty1
  late-commands:
    - |
      echo -e "\t    Kurulum durumu: BAGIMLILIKLAR OFFLINE OLARAK YUKLENIYOR..." > /dev/tty1

      # Copy the local package pool + repo index onto the target and
      # register it as a file:// apt source. apt then resolves install
      # order/dependencies itself, entirely offline, no network fallback.
      mkdir -p /target/opt/bizbox-pool
      cp -r /cdrom/pool/* /target/opt/bizbox-pool/
      curtin in-target -- bash -c "echo 'deb [trusted=yes] file:/opt/bizbox-pool ./' > /etc/apt/sources.list.d/bizbox-offline.list"
      curtin in-target -- apt-get update -o Dir::Etc::sourcelist="sources.list.d/bizbox-offline.list" -o Dir::Etc::sourceparts="-" -o APT::Get::List-Cleanup="0"
      curtin in-target -- apt-get install -y --no-install-recommends \
        openvswitch-switch zfsutils-linux incus incus-client sqlite3 iptables curl
      curtin in-target -- rm -f /etc/apt/sources.list.d/bizbox-offline.list
      curtin in-target -- rm -rf /opt/bizbox-pool

      echo -e "\t    Kurulum durumu: BIZBOX DOSYALARI KOPYALANIYOR..." > /dev/tty1
      mkdir -p /target/opt/bizbox
      cp -r /cdrom/payload/* /target/opt/bizbox/
      chmod +x /target/opt/bizbox/bizbox-mvp

      echo -e "\t    Kurulum durumu: ZFS DEPOLAMA ALANI YAPILANDIRILIYOR..." > /dev/tty1

      OS_DISK=\$(df /target | tail -1 | awk '{print \$1}' | grep -o '/dev/[a-zA-Z0-9]*' | head -n1)
      OS_DISK_NAME=\$(basename \$(readlink -f \$OS_DISK) | sed -E 's/([0-9]+|p[0-9]+)\$//')
      SECONDARY_DISK=\$(lsblk -dn -o NAME,TYPE | grep -E "disk" | awk '{print \$1}' | grep -v "\$OS_DISK_NAME" | head -n1)

      curtin in-target -- zpool destroy -f rft || true

      if [ -n "\$SECONDARY_DISK" ]; then
        wipefs -a -f "/dev/\$SECONDARY_DISK" || true
        dd if=/dev/zero of="/dev/\$SECONDARY_DISK" bs=1M count=50 conv=fdatasync || true
        curtin in-target -- zpool create -f rft "/dev/\$SECONDARY_DISK"
      else
        rm -f /target/var/lib/bizbox_zfs.img
        curtin in-target -- truncate -s 20G /var/lib/bizbox_zfs.img
        curtin in-target -- zpool create -f rft /var/lib/bizbox_zfs.img
      fi
      curtin in-target -- zfs create -p rft/virtual-machines || true
      curtin in-target -- zfs create -p rft/containers || true

      curtin in-target -- ovs-vsctl show | grep -q "br-int" || {
        curtin in-target -- ovs-vsctl add-br br-int
      }

      cat <<'SYS' > /target/etc/systemd/system/bizbox-mvp.service
      [Unit]
      Description=BizBox Hypervisor Manager
      After=network.target incus.service openvswitch-switch.service

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

      curtin in-target -- systemctl daemon-reload
      curtin in-target -- systemctl enable bizbox-mvp.service

      clear > /dev/tty1
      echo -e "\n\n\n\n\n\n" > /dev/tty1
      echo -e "\t############################################################" > /dev/tty1
      echo -e "\t#                                                          #" > /dev/tty1
      echo -e "\t#               KURULUM BASARIYLA TAMAMLANDI               #" > /dev/tty1
      echo -e "\t#                                                          #" > /dev/tty1
      echo -e "\t############################################################" > /dev/tty1
      echo -e "\n" > /dev/tty1
      echo -e "\t    Sistem simdi yeniden baslatiliyor." > /dev/tty1
      echo -e "\t    Yeniden basladiktan sonra tarayicinizdan erisebilirsiniz:" > /dev/tty1
      echo -e "\t    http://<sunucu-ip-adresi>:8080" > /dev/tty1
      echo -e "\n" > /dev/tty1
      sleep 8
EOF

# ---------------------------------------------------------------------------
# 8. Create custom grub.cfg
# ---------------------------------------------------------------------------
echo "Creating custom bootloader configuration..."
cat <<'GRUB' > "$TEMP_DIR/grub.cfg"
set timeout=3
set default=0

loadfont unicode

search --no-floppy --file --set=installed_os /boot/grub/grub.cfg

if [ -n "${installed_os}" ]; then
    menuentry "Boot from Local Disk (BizBox OS)" {
        set root="${installed_os}"
        configfile /boot/grub/grub.cfg
    }
fi

menuentry "BizBox Installer by ARIOT (Auto-install)" {
	set gfxpayload=keep
	linux	/casper/vmlinuz quiet autoinstall ds=nocloud\;s=/cdrom/nocloud/ console=ttyS0 ---
	initrd	/casper/initrd
}
GRUB

# ---------------------------------------------------------------------------
# 9. Remaster the ISO retaining all original boot capabilities
#    (hybrid MBR/GPT + EFI).
#    FIX: /boot/grub/grub.cfg already exists in the source ISO, so it must
#    be removed before it can be mapped again, otherwise xorriso aborts with
#    "already existing". Also use "-boot_image any patch" (not "replay"),
#    which is the documented sequence for modifying an existing boot-enabled
#    ISO in place.
# ---------------------------------------------------------------------------
echo "Remastering bootable ISO..."
rm -f "$CUSTOM_ISO"
xorriso -indev "$ORIGINAL_ISO" \
  -outdev "$CUSTOM_ISO" \
  -boot_image any keep \
  -rm_r /boot/grub/grub.cfg \
  -map "$TEMP_DIR/grub.cfg" /boot/grub/grub.cfg \
  -map "$TEMP_DIR/payload" /payload \
  -map "$TEMP_DIR/pool" /pool \
  -map "$TEMP_DIR/nocloud" /nocloud \
  -boot_image any patch

# Cleanup
rm -rf "$TEMP_DIR"

echo "====== ISO Build Completed Successfully: $CUSTOM_ISO ======"
echo "Target admin password: $TARGET_ADMIN_PASSWORD (please change on first login)"