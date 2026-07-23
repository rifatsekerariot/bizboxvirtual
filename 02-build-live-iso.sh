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

# Robust Kernel & Initrd resolution with automatic fallback generation
KERNEL_PATH=$(ls "$ROOTFS_DIR"/boot/vmlinuz-* 2>/dev/null | sort -V | tail -n1 || true)
INITRD_PATH=$(ls "$ROOTFS_DIR"/boot/initrd.img-* "$ROOTFS_DIR"/boot/initrd* 2>/dev/null | sort -V | tail -n1 || true)

if [ -z "$KERNEL_PATH" ] || [ ! -f "$KERNEL_PATH" ]; then
  echo "Hata: /boot/vmlinuz-* imajı bulunamadı!"
  exit 1
fi

if [ -z "$INITRD_PATH" ] || [ ! -f "$INITRD_PATH" ]; then
  echo "Bilgi: initrd.img dosyası bulunamadı, chroot içinde update-initramfs çalıştırılıyor..."
  mount --bind /dev "$ROOTFS_DIR/dev" 2>/dev/null || true
  mount -t proc proc "$ROOTFS_DIR/proc" 2>/dev/null || true
  mount -t sysfs sysfs "$ROOTFS_DIR/sys" 2>/dev/null || true
  chroot "$ROOTFS_DIR" /bin/bash -c "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin update-initramfs -c -k all" || true
  umount -lf "$ROOTFS_DIR/dev" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/proc" 2>/dev/null || true
  umount -lf "$ROOTFS_DIR/sys" 2>/dev/null || true
  INITRD_PATH=$(ls "$ROOTFS_DIR"/boot/initrd.img-* "$ROOTFS_DIR"/boot/initrd* 2>/dev/null | sort -V | tail -n1 || true)
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