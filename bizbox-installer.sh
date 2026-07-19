#!/bin/bash
# bizbox-installer.sh
# Runs automatically at boot (via bizbox-installer.service). If we are NOT
# actually running from live/removable media, this is a no-op that disables
# itself permanently (so it's safe that the service stays "enabled" even
# after the live filesystem gets copied onto the target disk).

set -euo pipefail
exec >/dev/tty1 2>&1

clear
echo -e "\n\n"
echo -e "\t############################################################"
echo -e "\t#                                                          #"
echo -e "\t#                BIZBOX HYPERVISOR INSTALLER               #"
echo -e "\t#                        by ARIOT                          #"
echo -e "\t#                                                          #"
echo -e "\t############################################################\n"

# ---------------------------------------------------------------------------
# Bail out early if this is NOT a live boot (i.e. we are the already-
# installed target system booting normally). Disable the service so it
# never runs again, then exit cleanly.
# ---------------------------------------------------------------------------
if ! grep -q 'boot=casper' /proc/cmdline; then
  systemctl disable bizbox-installer.service 2>/dev/null || true
  exit 0
fi

echo -e "\t    Sanal hypervisor sistemi kuruluyor, lutfen bekleyin..."
echo -e "\t    [Bu islem birkac dakika surebilir]\n"

# Make sure udev has actually enumerated every block device and processed
# its queue before we touch any disk. zpool create waits up to 30s for udev
# to mark a newly written partition "ready"; if udev hasn't run yet (or
# hasn't finished its initial coldplug), that wait times out with errno 19
# ("cannot label ... failed to detect device partitions"). The systemd unit
# ordering (After=systemd-udev-settle.service) should already guarantee
# this, but we also do it explicitly here as a second safety net.
udevadm trigger --type=devices --action=add 2>/dev/null || true
udevadm settle --timeout=30 || true

# ---------------------------------------------------------------------------
# 1. Identify disks. Skip whatever the live medium itself is booted from.
# ---------------------------------------------------------------------------
LIVE_SRC_DISK=""
for d in $(lsblk -dn -o NAME,TYPE | awk '$2=="disk"{print $1}'); do
  if mount | grep -E "^/dev/${d}[0-9]*" | grep -q -E "/cdrom|/media|/run/live|/rofs"; then
    LIVE_SRC_DISK="$d"
  fi
done

mapfile -t CANDIDATE_DISKS < <(lsblk -dn -o NAME,TYPE,SIZE,ROTA | awk '$2=="disk"{print $1,$3,$4}')

TARGET_DISK=""
BEST_SIZE=0
for line in "${CANDIDATE_DISKS[@]}"; do
  name=$(echo "$line" | awk '{print $1}')
  [ "$name" = "$LIVE_SRC_DISK" ] && continue
  size_bytes=$(blockdev --getsize64 "/dev/$name" 2>/dev/null || echo 0)
  if [ "$size_bytes" -gt "$BEST_SIZE" ]; then
    BEST_SIZE=$size_bytes
    TARGET_DISK="$name"
  fi
done

if [ -z "$TARGET_DISK" ]; then
  echo -e "\t    HATA: Kurulum icin uygun disk bulunamadi!"
  sleep 10
  exit 1
fi

# Secondary disk for ZFS pool = largest remaining disk after target is picked
SECONDARY_DISK=""
BEST_SIZE=0
for line in "${CANDIDATE_DISKS[@]}"; do
  name=$(echo "$line" | awk '{print $1}')
  [ "$name" = "$LIVE_SRC_DISK" ] && continue
  [ "$name" = "$TARGET_DISK" ] && continue
  size_bytes=$(blockdev --getsize64 "/dev/$name" 2>/dev/null || echo 0)
  if [ "$size_bytes" -gt "$BEST_SIZE" ]; then
    BEST_SIZE=$size_bytes
    SECONDARY_DISK="$name"
  fi
done

echo -e "\t    Kurulum durumu: DISK TEMIZLENIYOR (/dev/$TARGET_DISK)..."

# Thoroughly wipe a disk: clears filesystem/partition signatures AND both
# the primary and backup GPT headers (the backup GPT sits in the last
# sectors of the disk - a plain "dd the first 50MB" never touches it, which
# previously left zpool unable to re-label the disk: "cannot label ...
# failed to detect device partitions"). Also forces the kernel to re-read
# the (now empty) partition table before anything tries to use the disk.
wipe_disk() {
  local disk="$1"
  wipefs -a -f "/dev/$disk" || true
  sgdisk --zap-all "/dev/$disk" >/dev/null 2>&1 || true
  dd if=/dev/zero of="/dev/$disk" bs=1M count=50 conv=fdatasync || true
  local size_bytes size_mb seek_mb
  size_bytes=$(blockdev --getsize64 "/dev/$disk" 2>/dev/null || echo 0)
  size_mb=$(( size_bytes / 1024 / 1024 ))
  seek_mb=$(( size_mb - 50 ))
  if [ "$seek_mb" -gt 0 ]; then
    dd if=/dev/zero of="/dev/$disk" bs=1M count=50 seek="$seek_mb" conv=fdatasync || true
  fi
  partprobe "/dev/$disk" 2>/dev/null || true
  blockdev --rereadpt "/dev/$disk" 2>/dev/null || true
  udevadm settle || true
}

wipe_disk "$TARGET_DISK"

# ---------------------------------------------------------------------------
# 2. Partition: ESP (if UEFI) + root. Falls back to BIOS-boot partition
#    layout when not booted via UEFI.
# ---------------------------------------------------------------------------
IS_UEFI=0
[ -d /sys/firmware/efi ] && IS_UEFI=1

if [ "$IS_UEFI" -eq 1 ]; then
  parted -s "/dev/$TARGET_DISK" mklabel gpt \
    mkpart ESP fat32 1MiB 513MiB \
    set 1 esp on \
    mkpart primary ext4 513MiB 100%
  ESP_PART="/dev/${TARGET_DISK}1"
  ROOT_PART="/dev/${TARGET_DISK}2"
  mkfs.vfat -F32 "$ESP_PART"
else
  parted -s "/dev/$TARGET_DISK" mklabel gpt \
    mkpart bios_grub 1MiB 2MiB \
    set 1 bios_grub on \
    mkpart primary ext4 2MiB 100%
  ROOT_PART="/dev/${TARGET_DISK}2"
fi
mkfs.ext4 -F "$ROOT_PART"

echo -e "\t    Kurulum durumu: DOSYALAR KOPYALANIYOR..."

# ---------------------------------------------------------------------------
# 3. Copy the running live filesystem straight onto the target disk. The
#    live squashfs overlay root IS the target system, so this is a plain
#    file copy - no package installation needed at install time at all.
# ---------------------------------------------------------------------------
mkdir -p /target
mount "$ROOT_PART" /target
if [ "$IS_UEFI" -eq 1 ]; then
  mkdir -p /target/boot/efi
  mount "$ESP_PART" /target/boot/efi
fi

# Stop services that mount FUSE/virtual filesystems under /var/lib before
# copying, and always exclude those paths regardless - lxcfs exposes fake
# /proc-style files (e.g. /var/lib/lxcfs/proc/swaps) that return ENODATA
# when rsync tries to read them, which previously aborted the whole install
# (rsync exit code 23 + set -e). None of this is persistent data anyway -
# it gets recreated by the services themselves on first real boot.
systemctl stop incus incus.socket lxcfs 2>/dev/null || true
umount -lf /var/lib/lxcfs 2>/dev/null || true

rsync -aAX --info=progress2 \
  --exclude=/proc --exclude=/sys --exclude=/dev --exclude=/run \
  --exclude=/mnt --exclude=/media --exclude=/target --exclude=/cdrom \
  --exclude=/tmp --exclude=/lost+found \
  --exclude=/var/lib/lxcfs \
  --exclude=/var/lib/incus/**/proc* \
  / /target/

mkdir -p /target/proc /target/sys /target/dev /target/run /target/tmp
mount --bind /dev /target/dev
mount --bind /dev/pts /target/dev/pts
mount -t proc proc /target/proc
mount -t sysfs sysfs /target/sys

# fstab
ROOT_UUID=$(blkid -s UUID -o value "$ROOT_PART")
{
  echo "UUID=$ROOT_UUID / ext4 defaults 0 1"
  if [ "$IS_UEFI" -eq 1 ]; then
    ESP_UUID=$(blkid -s UUID -o value "$ESP_PART")
    echo "UUID=$ESP_UUID /boot/efi vfat umask=0077 0 1"
  fi
} > /target/etc/fstab

echo -e "\t    Kurulum durumu: BOOTLOADER KURULUYOR..."

if [ "$IS_UEFI" -eq 1 ]; then
  # --no-nvram ve --removable parametrelerini ekleyerek GRUB'ı diske taşınabilir modda yazıyoruz
  chroot /target grub-install --target=x86_64-efi --efi-directory=/boot/efi --no-nvram --removable --recheck
else
  chroot /target grub-install --target=i386-pc --recheck "/dev/$TARGET_DISK"
fi
chroot /target update-grub

# Fresh machine-id (was copied from the live image, must be unique per host)
rm -f /target/etc/machine-id
chroot /target systemd-machine-id-setup

# This is now the real installed system: never run the installer again.
chroot /target systemctl disable bizbox-installer.service || true
rm -f /target/usr/local/sbin/bizbox-installer.sh

echo -e "\t    Kurulum durumu: ZFS DEPOLAMA ALANI YAPILANDIRILIYOR..."

# Return the correct partition device name for a disk + partition number,
# accounting for nvme-style disks (nvme0n1 -> nvme0n1p1) vs sd-style (sda -> sda1).
part_name() {
  local disk="$1" num="$2"
  if [[ "$disk" =~ [0-9]$ ]]; then
    echo "${disk}p${num}"
  else
    echo "${disk}${num}"
  fi
}

chroot /target zpool destroy -f rft 2>/dev/null || true
if [ -n "$SECONDARY_DISK" ]; then
  wipe_disk "$SECONDARY_DISK"

  # Pre-partition ourselves (single partition spanning the whole disk) so
  # zpool is handed a partition, not a raw disk. Letting zpool operate on a
  # raw disk makes it create its own GPT + partition internally, which
  # depends on udev creating the new partition node in time - this races
  # and fails in minimal live environments ("cannot label ... failed to
  # detect device partitions"). Doing it ourselves + explicitly settling
  # avoids that race entirely.
  parted -s "/dev/$SECONDARY_DISK" mklabel gpt mkpart primary 0% 100%
  partprobe "/dev/$SECONDARY_DISK" 2>/dev/null || true
  udevadm settle || true
  sleep 2

  SECONDARY_PART="/dev/$(part_name "$SECONDARY_DISK" 1)"
  # Wait for the partition device node to actually show up (belt-and-braces
  # on top of udevadm settle - some VM disk backends are still slower).
  for i in $(seq 1 15); do
    [ -b "$SECONDARY_PART" ] && break
    sleep 1
  done

  echo "DEBUG: zpool create -f rft $SECONDARY_PART (should end in a partition number, not be the raw disk)"
  chroot /target zpool create -f rft "$SECONDARY_PART"
else
  chroot /target truncate -s 20G /var/lib/bizbox_zfs.img
  chroot /target zpool create -f rft /var/lib/bizbox_zfs.img
fi
chroot /target zfs create -p rft/virtual-machines || true
chroot /target zfs create -p rft/containers || true

echo -e "\t    Kurulum durumu: OVS YAPILANDIRILIYOR..."
chroot /target ovs-vsctl show 2>/dev/null | grep -q "br-int" || \
  chroot /target ovs-vsctl add-br br-int || true

umount -lf /target/dev/pts /target/dev /target/proc /target/sys
[ "$IS_UEFI" -eq 1 ] && umount -lf /target/boot/efi
umount -lf /target

clear
echo -e "\n\n"
echo -e "\t############################################################"
echo -e "\t#                                                          #"
echo -e "\t#               KURULUM BASARIYLA TAMAMLANDI               #"
echo -e "\t#                                                          #"
echo -e "\t############################################################\n"
echo -e "\t    Sistem simdi yeniden baslatiliyor."
echo -e "\t    Yeniden basladiktan sonra tarayicinizdan erisebilirsiniz:"
echo -e "\t    http://<sunucu-ip-adresi>:8080\n"
sleep 8
reboot