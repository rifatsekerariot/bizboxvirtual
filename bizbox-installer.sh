#!/bin/bash
# bizbox-installer.sh
# Runs automatically at boot (via bizbox-installer.service).
# ConditionKernelCommandLine=boot=casper in the unit file ensures this
# script only runs from live media. But we also guard here defensively.

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
# Defensive check: bail if not running from live media
# ---------------------------------------------------------------------------
if ! grep -q 'boot=casper' /proc/cmdline; then
  systemctl disable bizbox-installer.service 2>/dev/null || true
  exit 0
fi

echo -e "\t    Sanal hypervisor sistemi kuruluyor, lutfen bekleyin..."
echo -e "\t    [Bu islem birkac dakika surebilir]\n"

# udev'in tum block cihazlari taramis olmasini bekle
udevadm trigger --type=devices --action=add 2>/dev/null || true
udevadm settle --timeout=30 || true

# ---------------------------------------------------------------------------
# 1. Disk tespiti: Live medium'un kendi diskini atla
# ---------------------------------------------------------------------------
LIVE_SRC_DISK=""
for d in $(lsblk -dn -o NAME,TYPE | awk '$2=="disk"{print $1}'); do
  if mount | grep -qE "^/dev/${d}[0-9p]*" | grep -qE "/cdrom|/media|/run/live|/rofs" 2>/dev/null; then
    LIVE_SRC_DISK="$d"
  fi
  # Alternatif kontrol: disk icin herhangi bir partition mount edilmis mi
  if lsblk -no MOUNTPOINTS "/dev/$d" 2>/dev/null | grep -qE "/cdrom|/run/live|/media"; then
    LIVE_SRC_DISK="$d"
  fi
done

mapfile -t CANDIDATE_DISKS < <(lsblk -dn -o NAME,TYPE | awk '$2=="disk"{print $1}')

TARGET_DISK=""
BEST_SIZE=0
for name in "${CANDIDATE_DISKS[@]}"; do
  [ "$name" = "$LIVE_SRC_DISK" ] && continue
  size_bytes=$(blockdev --getsize64 "/dev/$name" 2>/dev/null || echo 0)
  if [ "$size_bytes" -gt "$BEST_SIZE" ]; then
    BEST_SIZE=$size_bytes
    TARGET_DISK="$name"
  fi
done

if [ -z "$TARGET_DISK" ]; then
  echo -e "\t    HATA: Kurulum icin uygun disk bulunamadi!"
  echo -e "\t    Mevcut diskler:"
  lsblk -dn -o NAME,SIZE,TYPE
  sleep 30
  exit 1
fi

echo -e "\t    Hedef disk: /dev/$TARGET_DISK"

# Secondary disk: ZFS icin (OS diski disindaki en buyuk disk)
SECONDARY_DISK=""
BEST_SIZE=0
for name in "${CANDIDATE_DISKS[@]}"; do
  [ "$name" = "$LIVE_SRC_DISK" ] && continue
  [ "$name" = "$TARGET_DISK" ] && continue
  size_bytes=$(blockdev --getsize64 "/dev/$name" 2>/dev/null || echo 0)
  if [ "$size_bytes" -gt "$BEST_SIZE" ]; then
    BEST_SIZE=$size_bytes
    SECONDARY_DISK="$name"
  fi
done

[ -n "$SECONDARY_DISK" ] && echo -e "\t    ZFS disk: /dev/$SECONDARY_DISK" || echo -e "\t    ZFS: dosya tabanli (loopback)"

# ---------------------------------------------------------------------------
# Disk silme fonksiyonu: GPT basligi + imzalar + MBR temizlenir
# ---------------------------------------------------------------------------
wipe_disk() {
  local disk="$1"
  echo "    Siliniyor: /dev/$disk"
  wipefs -a -f "/dev/$disk" 2>/dev/null || true
  sgdisk --zap-all "/dev/$disk" >/dev/null 2>&1 || true
  dd if=/dev/zero of="/dev/$disk" bs=1M count=100 conv=fdatasync 2>/dev/null || true
  local size_bytes size_mb seek_mb
  size_bytes=$(blockdev --getsize64 "/dev/$disk" 2>/dev/null || echo 0)
  size_mb=$(( size_bytes / 1024 / 1024 ))
  seek_mb=$(( size_mb - 100 ))
  if [ "$seek_mb" -gt 0 ]; then
    dd if=/dev/zero of="/dev/$disk" bs=1M count=100 seek="$seek_mb" conv=fdatasync 2>/dev/null || true
  fi
  partprobe "/dev/$disk" 2>/dev/null || true
  blockdev --rereadpt "/dev/$disk" 2>/dev/null || true
  udevadm settle --timeout=10 || true
}

echo -e "\t    Kurulum durumu: DISK TEMIZLENIYOR (/dev/$TARGET_DISK)..."
wipe_disk "$TARGET_DISK"

# ---------------------------------------------------------------------------
# 2. Bolum olusturma
# ---------------------------------------------------------------------------
IS_UEFI=0
[ -d /sys/firmware/efi ] && IS_UEFI=1

part_name() {
  local disk="$1" num="$2"
  if [[ "$disk" =~ [0-9]$ ]]; then
    echo "${disk}p${num}"
  else
    echo "${disk}${num}"
  fi
}

if [ "$IS_UEFI" -eq 1 ]; then
  echo -e "\t    Mod: UEFI"
  parted -s "/dev/$TARGET_DISK" mklabel gpt \
    mkpart ESP fat32 1MiB 513MiB \
    set 1 esp on \
    mkpart primary ext4 513MiB 100%
  ESP_PART="/dev/$(part_name "$TARGET_DISK" 1)"
  ROOT_PART="/dev/$(part_name "$TARGET_DISK" 2)"
  udevadm settle --timeout=10 || true
  sleep 2
  mkfs.vfat -F32 "$ESP_PART"
else
  echo -e "\t    Mod: BIOS/Legacy"
  parted -s "/dev/$TARGET_DISK" mklabel gpt \
    mkpart bios_grub 1MiB 2MiB \
    set 1 bios_grub on \
    mkpart primary ext4 2MiB 100%
  ROOT_PART="/dev/$(part_name "$TARGET_DISK" 2)"
  udevadm settle --timeout=10 || true
  sleep 2
fi

mkfs.ext4 -F "$ROOT_PART"
udevadm settle --timeout=10 || true

echo -e "\t    Kurulum durumu: DOSYALAR KOPYALANIYOR..."

# ---------------------------------------------------------------------------
# 3. Live rootfs -> hedef disk (rsync)
# ---------------------------------------------------------------------------
mkdir -p /target
mount "$ROOT_PART" /target
if [ "$IS_UEFI" -eq 1 ]; then
  mkdir -p /target/boot/efi
  mount "$ESP_PART" /target/boot/efi
fi

# lxcfs / incus sanal dosya sistemleri rsync'i kiratiyor — durdur
systemctl stop incus incus.socket lxcfs 2>/dev/null || true
umount -lf /var/lib/lxcfs 2>/dev/null || true

rsync -aAX --info=progress2 \
  --exclude=/proc --exclude=/sys --exclude=/dev --exclude=/run \
  --exclude=/mnt --exclude=/media --exclude=/target --exclude=/cdrom \
  --exclude=/tmp --exclude=/lost+found \
  --exclude=/var/lib/lxcfs \
  --exclude=/var/lib/incus/virtual-machines \
  --exclude=/var/lib/incus/containers \
  / /target/

mkdir -p /target/{proc,sys,dev,run,tmp}
chmod 1777 /target/tmp

mount --bind /dev      /target/dev
mount --bind /dev/pts  /target/dev/pts
mount -t proc  proc    /target/proc
mount -t sysfs sysfs   /target/sys

# ---------------------------------------------------------------------------
# 3b. Kernel ve initrd: squashfs /boot icermez
#     Live medium'dan kopyala (casper vmlinuz/initrd olarak konumlanir)
# ---------------------------------------------------------------------------
mkdir -p /target/boot

KVER=$(ls /target/lib/modules/ 2>/dev/null | sort -V | tail -n1 \
     || ls /lib/modules/ 2>/dev/null | sort -V | tail -n1 \
     || uname -r)

KERNEL_FILE="vmlinuz-${KVER}"
INITRD_FILE="initrd.img-${KVER}"

# Live medium mount noktasini bul (casper /cdrom, debian /run/live/medium)
LIVE_MEDIUM=""
for candidate in /cdrom /run/live/medium /media/cdrom /media/cdrom0; do
  if [ -f "${candidate}/casper/vmlinuz" ]; then
    LIVE_MEDIUM="$candidate"
    break
  fi
done

if [ -z "$LIVE_MEDIUM" ]; then
  echo -e "\t    UYARI: Live medium bulunamadi! Kernel initramfs'ten olusturuluyor..."
  chroot /target update-initramfs -c -k "$KVER" 2>/dev/null || true
  # Kernel dosyasini mevcut sistemden al
  if [ -f "/boot/vmlinuz-${KVER}" ]; then
    cp "/boot/vmlinuz-${KVER}" "/target/boot/${KERNEL_FILE}"
  fi
else
  echo -e "\t    Live medium: $LIVE_MEDIUM"
  [ ! -f "/target/boot/$KERNEL_FILE" ] && cp "${LIVE_MEDIUM}/casper/vmlinuz" "/target/boot/$KERNEL_FILE"
  [ ! -f "/target/boot/$INITRD_FILE" ] && cp "${LIVE_MEDIUM}/casper/initrd"  "/target/boot/$INITRD_FILE"
fi

# Son kontrol
if [ ! -f "/target/boot/$KERNEL_FILE" ]; then
  echo -e "\t    HATA: Kernel /target/boot/ icinde yok!"
  ls /target/boot/ || true
  exit 1
fi

echo -e "\t    Kernel: $KERNEL_FILE"
echo -e "\t    Initrd: $INITRD_FILE"

# ---------------------------------------------------------------------------
# fstab
# ---------------------------------------------------------------------------
ROOT_UUID=$(blkid -s UUID -o value "$ROOT_PART")
{
  echo "UUID=$ROOT_UUID / ext4 errors=remount-ro 0 1"
  if [ "$IS_UEFI" -eq 1 ]; then
    ESP_UUID=$(blkid -s UUID -o value "$ESP_PART")
    echo "UUID=$ESP_UUID /boot/efi vfat umask=0077 0 1"
  fi
  echo "tmpfs /tmp tmpfs defaults,nosuid,nodev 0 0"
} > /target/etc/fstab

# ---------------------------------------------------------------------------
# 4. Bootloader
# ---------------------------------------------------------------------------
echo -e "\t    Kurulum durumu: BOOTLOADER KURULUYOR..."

if [ "$IS_UEFI" -eq 1 ]; then
  chroot /target grub-install \
    --target=x86_64-efi \
    --efi-directory=/boot/efi \
    --bootloader-id=BizBox \
    --removable \
    --recheck
else
  chroot /target grub-install \
    --target=i386-pc \
    --recheck \
    "/dev/$TARGET_DISK"
fi

# grub.cfg: dogrudan yaz (update-grub live parametrelerini kopyalar - kullanma)
mkdir -p /target/boot/grub
cat > /target/boot/grub/grub.cfg << GRUBCFG
set default=0
set timeout=5

loadfont unicode

search --no-floppy --fs-uuid --set=root $ROOT_UUID

menuentry "BizBox Hypervisor" {
    search --no-floppy --fs-uuid --set=root $ROOT_UUID
    linux  /boot/$KERNEL_FILE root=UUID=$ROOT_UUID ro nomodeset noapic quiet
    initrd /boot/$INITRD_FILE
}
GRUBCFG

# ---------------------------------------------------------------------------
# machine-id: kopya yerine yenisini uret
# ---------------------------------------------------------------------------
rm -f /target/etc/machine-id
chroot /target systemd-machine-id-setup

# Installer servisi kurulu sistemde tekrar calismasin
chroot /target systemctl disable bizbox-installer.service 2>/dev/null || true
rm -f /target/usr/local/sbin/bizbox-installer.sh

# ---------------------------------------------------------------------------
# 5. ZFS depolama alani
# ---------------------------------------------------------------------------
echo -e "\t    Kurulum durumu: ZFS DEPOLAMA ALANI YAPILANDIRILIYOR..."

chroot /target zpool destroy -f rft 2>/dev/null || true

if [ -n "$SECONDARY_DISK" ]; then
  wipe_disk "$SECONDARY_DISK"
  parted -s "/dev/$SECONDARY_DISK" mklabel gpt mkpart primary 0% 100%
  partprobe "/dev/$SECONDARY_DISK" 2>/dev/null || true
  udevadm settle --timeout=10 || true
  sleep 3

  SECONDARY_PART="/dev/$(part_name "$SECONDARY_DISK" 1)"
  # Partition node olusana kadar bekle
  for i in $(seq 1 20); do
    [ -b "$SECONDARY_PART" ] && break
    sleep 1
  done

  if [ -b "$SECONDARY_PART" ]; then
    chroot /target zpool create -f rft "$SECONDARY_PART"
  else
    echo -e "\t    UYARI: Secondary disk partition bulunamadi, loopback'e geciyor..."
    chroot /target truncate -s 20G /var/lib/bizbox_zfs.img
    chroot /target zpool create -f rft /var/lib/bizbox_zfs.img
  fi
else
  rm -f /target/var/lib/bizbox_zfs.img
  chroot /target truncate -s 20G /var/lib/bizbox_zfs.img
  chroot /target zpool create -f rft /var/lib/bizbox_zfs.img
fi

chroot /target zfs create -p rft/virtual-machines 2>/dev/null || true
chroot /target zfs create -p rft/containers       2>/dev/null || true

# ---------------------------------------------------------------------------
# 6. Open vSwitch
# ---------------------------------------------------------------------------
echo -e "\t    Kurulum durumu: OVS YAPILANDIRILIYOR..."
chroot /target ovs-vsctl show 2>/dev/null | grep -q "br-int" || \
  chroot /target ovs-vsctl add-br br-int 2>/dev/null || true

# ---------------------------------------------------------------------------
# Cleanup ve reboot
# ---------------------------------------------------------------------------
umount -lf /target/dev/pts  2>/dev/null || true
umount -lf /target/dev      2>/dev/null || true
umount -lf /target/proc     2>/dev/null || true
umount -lf /target/sys      2>/dev/null || true
[ "$IS_UEFI" -eq 1 ] && umount -lf /target/boot/efi 2>/dev/null || true
sync
umount /target

clear
echo -e "\n\n"
echo -e "\t############################################################"
echo -e "\t#                                                          #"
echo -e "\t#               KURULUM BASARIYLA TAMAMLANDI               #"
echo -e "\t#                                                          #"
echo -e "\t############################################################\n"
echo -e "\t    Kullanici adi : admin"
echo -e "\t    Parola        : admin\n"
echo -e "\t    Sistem 8 saniye sonra yeniden baslatilacak."
echo -e "\t    Tarayicinizdan erisim: http://<ip-adresi>:8080\n"
sleep 8
reboot