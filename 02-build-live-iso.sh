#!/bin/bash
# 02-build-live-iso.sh
# Takes the rootfs produced by 01-build-rootfs.sh, compresses it into a
# squashfs, and builds a bootable BIOS+UEFI ISO using grub-mkrescue (which
# handles the hybrid El Torito/EFI boot catalog for us - no manual xorriso
# eltorito flags needed).

set -euo pipefail

ROOTFS_DIR="$(pwd)/rootfs"
ISO_TREE="$(pwd)/iso-tree"
CUSTOM_ISO="bizbox-installer.iso"

if [ ! -d "$ROOTFS_DIR" ]; then
  echo "rootfs bulunamadi. Once 01-build-rootfs.sh calistirin."
  exit 1
fi

echo "====== [1/4] Preparing ISO tree ======"
rm -rf "$ISO_TREE"
mkdir -p "$ISO_TREE/casper" "$ISO_TREE/boot/grub"

# Helper to find the newest existing, non-broken regular file matching a glob
find_valid_file() {
  local found=""
  for candidate in $(ls -1 $1 2>/dev/null | sort -rV); do
    if [ -f "$candidate" ] && [ -s "$candidate" ]; then
      found="$candidate"
      break
    fi
  done
  echo "$found"
}

KERNEL_PATH=$(find_valid_file "$ROOTFS_DIR/boot/vmlinuz-*")
INITRD_PATH=$(find_valid_file "$ROOTFS_DIR/boot/initrd.img-*")

if [ -z "$KERNEL_PATH" ]; then
  echo "Hata: /boot/vmlinuz-* imajı bulunamadı!"
  exit 1
fi

if [ -z "$INITRD_PATH" ]; then
  echo "Bilgi: initrd.img dosyası bulunamadı, chroot ortamı hazırlanıyor ve initramfs derleniyor..."
  mkdir -p "$ROOTFS_DIR/dev/pts" "$ROOTFS_DIR/proc" "$ROOTFS_DIR/sys" "$ROOTFS_DIR/etc"

  mount -t proc proc "$ROOTFS_DIR/proc" 2>/dev/null || true
  mount -t sysfs sysfs "$ROOTFS_DIR/sys" 2>/dev/null || true
  mount -t devpts devpts "$ROOTFS_DIR/dev/pts" 2>/dev/null || true

  # Populate essential static dev nodes inside rootfs
  [ -c "$ROOTFS_DIR/dev/null" ] || mknod -m 666 "$ROOTFS_DIR/dev/null" c 1 3 2>/dev/null || true
  [ -c "$ROOTFS_DIR/dev/zero" ] || mknod -m 666 "$ROOTFS_DIR/dev/zero" c 1 5 2>/dev/null || true
  [ -c "$ROOTFS_DIR/dev/random" ] || mknod -m 666 "$ROOTFS_DIR/dev/random" c 1 8 2>/dev/null || true
  [ -c "$ROOTFS_DIR/dev/urandom" ] || mknod -m 666 "$ROOTFS_DIR/dev/urandom" c 1 9 2>/dev/null || true
  [ -c "$ROOTFS_DIR/dev/ptmx" ] || mknod -m 666 "$ROOTFS_DIR/dev/ptmx" c 5 2 2>/dev/null || true

  cat <<EOF > "$ROOTFS_DIR/etc/resolv.conf"
nameserver 8.8.8.8
nameserver 1.1.1.1
EOF

  # Check if update-initramfs exists, if not install initramfs-tools dynamically inside rootfs
  chroot "$ROOTFS_DIR" /bin/bash -c "
    set -e
    export DEBIAN_FRONTEND=noninteractive
    if ! command -v update-initramfs &>/dev/null; then
      apt-get update -qq
      apt-get install -y --no-install-recommends initramfs-tools
    fi
    update-initramfs -c -k all || update-initramfs -u -k all
  " || true

  umount -lf "$ROOTFS_DIR/dev/pts" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/proc" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/sys" 2>/dev/null || true

  INITRD_PATH=$(find_valid_file "$ROOTFS_DIR/boot/initrd.img-*")
  if [ -z "$INITRD_PATH" ]; then
    INITRD_PATH=$(find_valid_file "$ROOTFS_DIR/boot/initrd*")
  fi
fi

if [ -z "$INITRD_PATH" ]; then
  echo "Hata: initrd.img dosyası oluşturulamadı veya bulunamadı!"
  exit 1
fi

echo "Kernel: $KERNEL_PATH"
echo "Initrd: $INITRD_PATH"

cp "$KERNEL_PATH" "$ISO_TREE/casper/vmlinuz"
cp "$INITRD_PATH" "$ISO_TREE/casper/initrd"

echo "====== [2/4] Compressing rootfs to squashfs (xz, this takes a while) ======"
mksquashfs "$ROOTFS_DIR" "$ISO_TREE/casper/filesystem.squashfs" \
  -comp xz \
  -noappend

printf "%s" "$(du -sx --block-size=1 "$ROOTFS_DIR" | cut -f1)" > "$ISO_TREE/casper/filesystem.size"

echo "====== [3/4] Writing grub.cfg ======"
# GRUB'ın dosyayı kesin bulabilmesi için standart yolları oluşturuyoruz
mkdir -p "$ISO_TREE/boot/grub"
mkdir -p "$ISO_TREE/EFI/BOOT"

cat > "$ISO_TREE/boot/grub/grub.cfg" <<'GRUB'
set timeout=5
set default=0

menuentry "BizBox Installer by ARIOT" {
    search --no-floppy --file --set=root /casper/vmlinuz
    linux /casper/vmlinuz boot=casper nomodeset noapic quiet ---
    initrd /casper/initrd
}

menuentry "BizBox Installer (debug - verbose)" {
    search --no-floppy --file --set=root /casper/vmlinuz
    linux /casper/vmlinuz boot=casper nomodeset noapic
    initrd /casper/initrd
}
GRUB

# UEFI modunun doğrudan kök dizinde grub.cfg arama ihtimaline karşı kopyalıyoruz
cp "$ISO_TREE/boot/grub/grub.cfg" "$ISO_TREE/grub.cfg"

echo "====== [4/4] Building hybrid BIOS+UEFI ISO ======"
rm -f "$CUSTOM_ISO"
# -- -volid BIZBOX parametresi yukarıdaki search komutunun ISO'yu tanımasını sağlar
grub-mkrescue -o "$CUSTOM_ISO" "$ISO_TREE" -- -volid BIZBOX 2>&1 | grep -v "^xorriso :" || true

rm -rf "$ISO_TREE"

echo "====== ISO Build Completed: $CUSTOM_ISO ======"
ls -lh "$CUSTOM_ISO"