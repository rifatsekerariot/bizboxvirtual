#!/bin/bash
# BizBox Automated ISO Builder
# Run this on your Linux server as root.

set -e

# Configuration
LATEST_ISO_FILE=$(curl -s https://releases.ubuntu.com/24.04/ | grep -oE 'ubuntu-24.04\.[0-9]+-live-server-amd64\.iso' | head -n 1)
if [ -z "$LATEST_ISO_FILE" ]; then
  LATEST_ISO_FILE="ubuntu-24.04.1-live-server-amd64.iso"
fi
UBUNTU_ISO_URL="https://releases.ubuntu.com/24.04/$LATEST_ISO_FILE"
ORIGINAL_ISO="$LATEST_ISO_FILE"
CUSTOM_ISO="bizbox-installer.iso"
TEMP_DIR="./iso-temp-files"

echo "====== Starting BizBox ISO Build ======"

# 1. Install required packages on build host
echo "Installing build dependencies..."
apt-get update
apt-get install -y xorriso squashfs-tools wget curl golang-go git

# 2. Compile BizBox Go Application
echo "Compiling BizBox application..."
cd bizbox-mvp
go build -buildvcs=false -o bizbox-mvp
cd ..

# 3. Download Ubuntu Base ISO if not present
if [ ! -f "$ORIGINAL_ISO" ]; then
  echo "Downloading base Ubuntu Server ISO ($ORIGINAL_ISO)..."
  wget -O "$ORIGINAL_ISO" "$UBUNTU_ISO_URL"
fi

# 4. Prepare temporary directory structure
echo "Preparing temporary directories..."
rm -rf "$TEMP_DIR"
mkdir -p "$TEMP_DIR/payload"
mkdir -p "$TEMP_DIR/nocloud"

# 5. Copy payload (compiled binary and assets)
echo "Preparing BizBox payload..."
cp -r bizbox-mvp/bizbox-mvp bizbox-mvp/static bizbox-mvp/templates "$TEMP_DIR/payload/"

# 6. Create Autoinstall configuration (user-data & meta-data)
echo "Creating Autoinstall configurations..."
touch "$TEMP_DIR/nocloud/meta-data"

cat <<'EOF' > "$TEMP_DIR/nocloud/user-data"
#cloud-config
autoinstall:
  version: 1
  identity:
    hostname: bizbox-host
    password: "$6$expen5116$85z5B3Q2tQeHnK7fM45a.lG6V/yD/u9tQoK41jB6b2u3x2e3B9uD2yQ0eHnK7fM45a.lG6V/yD/u9tQoK41jB6b" # password: admin
    username: admin
  locale: en_US.UTF-8
  keyboard:
    layout: us
  storage:
    layout:
      name: direct
  packages:
    - openvswitch-switch
    - zfsutils-linux
    - incus
    - incus-client
    - sqlite3
    - tc
    - iptables
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
      echo -e "\n\t    Kurulum durumu: PAKETLER VE BAGIMLILIKLAR YUKLENIYOR..." > /dev/tty1
  late-commands:
    - |
      echo -e "\t    Kurulum durumu: BIZBOX DOSYALARI KOPYALANIYOR VE DERLENIYOR..." > /dev/tty1
      # Copy application payload
      mkdir -p /target/opt/bizbox
      cp -r /cdrom/payload/* /target/opt/bizbox/
      chmod +x /target/opt/bizbox/bizbox-mvp

      # Configure ZFS Storage Pool on target
      curtin in-target -- zpool list | grep -q "rft" || {
        # Find OS disk partition (active on /target)
        OS_DISK=$(df /target | tail -1 | awk '{print $1}' | grep -o '/dev/[a-zA-Z0-9]*' | head -n1)
        # Resolve to disk base name (e.g. sda, nvme0n1)
        OS_DISK_NAME=$(basename $(readlink -f $OS_DISK) | sed -E 's/([0-9]+|p[0-9]+)$//')

        # Find first disk device that is not the OS disk
        SECONDARY_DISK=$(lsblk -dn -o NAME,TYPE | grep -E "disk" | awk '{print $1}' | grep -v "$OS_DISK_NAME" | head -n1)

        if [ -n "$SECONDARY_DISK" ]; then
          # Wipe secondary disk partition signatures to avoid ZFS errors
          dd if=/dev/zero of="/dev/$SECONDARY_DISK" bs=1M count=10 conv=fdatasync || true
          # Create ZFS pool on secondary disk directly
          curtin in-target -- zpool create -f rft "/dev/$SECONDARY_DISK"
        else
          # Fallback to loop image on the OS partition if no other disk exists
          curtin in-target -- truncate -s 20G /var/lib/bizbox_zfs.img
          curtin in-target -- zpool create rft /var/lib/bizbox_zfs.img
        fi
      }
      curtin in-target -- zfs create -p rft/virtual-machines || true
      curtin in-target -- zfs create -p rft/containers || true

      # Configure OVS
      curtin in-target -- ovs-vsctl show | grep -q "br-int" || {
        curtin in-target -- ovs-vsctl add-br br-int
      }

      # Create systemd service
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

      # Enable systemd service
      curtin in-target -- systemctl daemon-reload
      curtin in-target -- systemctl enable bizbox-mvp.service

      # Final message
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

# 7. Create custom grub.cfg
echo "Creating custom bootloader configuration..."
cat <<'GRUB' > "$TEMP_DIR/grub.cfg"
set timeout=1
set default=0

loadfont unicode

menuentry "BizBox Installer by ARIOT" {
	set gfxpayload=keep
	linux	/casper/vmlinuz quiet autoinstall ds=nocloud;s=/cdrom/nocloud/ console=ttyS0 ---
	initrd	/casper/initrd
}
GRUB

# 8. Remaster the ISO retaining all original boot capabilities (hybrid MBR/GPT + EFI)
echo "Remastering bootable ISO..."
rm -f "$CUSTOM_ISO"
xorriso -dev "$ORIGINAL_ISO" \
  -boot_image any keep \
  -outdev "$CUSTOM_ISO" \
  -map "$TEMP_DIR/payload" /payload \
  -map "$TEMP_DIR/nocloud" /nocloud \
  -map "$TEMP_DIR/grub.cfg" /boot/grub/grub.cfg \
  -boot_image any replay

# Cleanup
rm -rf "$TEMP_DIR"

echo "====== ISO Build Completed Successfully: $CUSTOM_ISO ======"
