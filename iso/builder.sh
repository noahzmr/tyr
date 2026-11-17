#!/usr/bin/env bash
# Runs inside the build container (iso/Dockerfile) for one architecture:
# Debian root filesystem with Tyr and its tools → squashfs → bootable ISO
# (BIOS + UEFI on amd64, UEFI on arm64; Secure Boot through Debian's signed
# shim and GRUB).
set -euo pipefail

arch=$1
version=${VERSION:-dev}
mirror=${MIRROR:-http://deb.debian.org/debian}
work=/tmp/tyr-iso
out=/src/dist/tyr-live-$version-$arch.iso
rm -rf "$work"
mkdir -p "$work/staging/live" "$work/staging/boot/grub" "$work/staging/.disk"

# what the live system needs: kernel, live-boot, Tyr's tools, firmware for
# server NICs and controllers. No mdadm, no LVM: nothing may assemble or
# touch the server's disks.
pkgs=(
	"linux-image-$arch" live-boot systemd-sysv systemd-timesyncd udev kmod
	util-linux bsdextrautils procps iproute2 kbd console-setup less vim-tiny bash-completion
	pciutils usbutils ethtool dmidecode smartmontools nvme-cli sg3-utils ipmitool rasdaemon lldpd
	stress-ng memtester iperf3 exfatprogs dosfstools openssh-client ca-certificates
	firmware-linux-free firmware-misc-nonfree firmware-bnx2 firmware-bnx2x firmware-qlogic
	firmware-realtek firmware-netxen firmware-myricom firmware-cavium firmware-intel-misc
)
case "$arch" in
amd64) pkgs+=(amd64-microcode intel-microcode memtest86+ firmware-amd-graphics) ;;
arm64) pkgs+=() ;;
esac

vendor_hook=()
shopt -s nullglob
debs=(iso/vendor/*.deb)
rpms=(iso/vendor/*.rpm)
if ((${#debs[@]})); then
	echo "vendor packages: ${debs[*]}"
	vendor_hook+=(--customize-hook="mkdir -p \"\$1/tmp/vendor\" && cp ${debs[*]} \"\$1/tmp/vendor/\""
		--customize-hook='chroot "$1" sh -c "DEBIAN_FRONTEND=noninteractive apt-get install -y /tmp/vendor/*.deb" && rm -rf "$1/tmp/vendor"')
fi
for r in "${rpms[@]}"; do
	echo "vendor rpm (unpacked): $r"
	vendor_hook+=(--customize-hook="rpm2cpio $r | (cd \"\$1\" && cpio -idm --quiet)")
done

limits_hook=()
if [[ -f iso/accept.json ]]; then
	echo "limits: iso/accept.json"
	/src/dist/tyr-linux-"$arch" accept --limits iso/accept.json --print-profile >/dev/null
	limits_hook=(--customize-hook='cp iso/accept.json "$1/etc/tyr/accept.json"')
fi

echo "== root filesystem (Debian trixie, $arch)"
mmdebstrap --mode=root --variant=minbase --architectures="$arch" \
	--components="main non-free-firmware" \
	--include="$(
		IFS=,
		echo "${pkgs[*]}"
	)" \
	--aptopt='APT::Install-Recommends "false"' \
	--dpkgopt='path-exclude=/usr/share/doc/*' --dpkgopt='path-include=/usr/share/doc/*/copyright' \
	--dpkgopt='path-exclude=/usr/share/man/*' --dpkgopt='path-exclude=/usr/share/locale/*' \
	--customize-hook='sync-in iso/overlay /' \
	--customize-hook="install -m 755 dist/tyr-linux-$arch \"\$1/usr/local/bin/tyr\"" \
	"${limits_hook[@]}" "${vendor_hook[@]}" \
	--customize-hook='chroot "$1" sh -e /usr/local/sbin/tyr-live-setup' \
	--customize-hook='chroot "$1" update-initramfs -u -k all' \
	trixie "$work/rootfs" "deb $mirror trixie main non-free-firmware" \
	"deb $mirror trixie-updates main non-free-firmware" \
	"deb http://deb.debian.org/debian-security trixie-security main non-free-firmware"

echo "== squashfs"
cp "$work"/rootfs/boot/vmlinuz-* "$work/staging/live/vmlinuz"
cp "$work"/rootfs/boot/initrd.img-* "$work/staging/live/initrd.img"
if [[ $arch == amd64 ]]; then
	cp "$work/rootfs/boot/memtest86+x64.bin" "$work/rootfs/boot/memtest86+x64.efi" "$work/staging/boot/"
fi
rm -rf "$work"/rootfs/var/cache/apt/archives/*.deb "$work"/rootfs/var/lib/apt/lists/*
bcj=x86
[[ $arch == arm64 ]] && bcj=arm
mksquashfs "$work/rootfs" "$work/staging/live/filesystem.squashfs" -comp xz -Xbcj "$bcj" -b 1M -noappend -no-xattrs -quiet -e boot

echo "== boot loader"
echo "Tyr live $version $arch $(date -u +%Y-%m-%d)" >"$work/staging/.disk/info"
sed "s/@VERSION@/$version/g; s/@ARCH@/$arch/g" iso/grub.cfg >"$work/staging/boot/grub/grub.cfg"

# EFI system partition: shim (signed by Microsoft) loads Debian's signed
# GRUB for CDs (gcd*), which finds /.disk/info and reads /boot/grub/grub.cfg.
case "$arch" in
amd64) efi=x64 ;;
arm64) efi=aa64 ;;
esac
esp=$work/efiboot.img
mkfs.vfat -C -n TYR_EFI "$esp" 8192 >/dev/null
mmd -i "$esp" ::/EFI ::/EFI/boot
mcopy -i "$esp" "/usr/lib/shim/shim$efi.efi.signed" "::/EFI/boot/boot$efi.efi"
mcopy -i "$esp" "/usr/lib/shim/mm$efi.efi.signed" "::/EFI/boot/mm$efi.efi"
grubdir=/usr/lib/grub/$([[ $arch == amd64 ]] && echo x86_64 || echo arm64)-efi-signed
mcopy -i "$esp" "$grubdir/gcd$efi.efi.signed" "::/EFI/boot/grub$efi.efi"
# the same tree in the ISO filesystem, for tools that copy the ISO to a stick
mkdir -p "$work/staging/EFI/boot"
cp "/usr/lib/shim/shim$efi.efi.signed" "$work/staging/EFI/boot/boot$efi.efi"
cp "/usr/lib/shim/mm$efi.efi.signed" "$work/staging/EFI/boot/mm$efi.efi"
cp "$grubdir/gcd$efi.efi.signed" "$work/staging/EFI/boot/grub$efi.efi"

mkdir -p "$(dirname "$out")"
if [[ $arch == amd64 ]]; then
	# BIOS: GRUB El Torito image; the hybrid MBR also boots it from a USB stick
	grub-mkimage -O i386-pc-eltorito -p /boot/grub -o "$work/staging/boot/grub/bios.img" \
		biosdisk iso9660 part_msdos part_gpt normal search search_fs_file configfile linux linux16 \
		echo test ls cat reboot halt sleep all_video
	xorriso -as mkisofs -iso-level 3 -full-iso9660-filenames -joliet -joliet-long -rational-rock \
		-volid TYR_LIVE -appid "Tyr live $version" -o "$out" \
		--grub2-mbr /usr/lib/grub/i386-pc/boot_hybrid.img --mbr-force-bootable -partition_offset 16 \
		-eltorito-boot boot/grub/bios.img -no-emul-boot -boot-load-size 4 -boot-info-table --grub2-boot-info \
		-eltorito-catalog boot/grub/boot.cat \
		-eltorito-alt-boot -e --interval:appended_partition_2:all:: -no-emul-boot \
		-append_partition 2 C12A7328-F81F-11D2-BA4B-00A0C93EC93B "$esp" \
		"$work/staging"
else
	xorriso -as mkisofs -iso-level 3 -full-iso9660-filenames -joliet -joliet-long -rational-rock \
		-volid TYR_LIVE -appid "Tyr live $version" -o "$out" \
		-e --interval:appended_partition_2:all:: -no-emul-boot \
		-append_partition 2 C12A7328-F81F-11D2-BA4B-00A0C93EC93B "$esp" \
		"$work/staging"
fi
(cd "$(dirname "$out")" && sha256sum "$(basename "$out")" >"$(basename "$out").sha256")
chown "${HOST_UID:-0}:${HOST_GID:-0}" "$out" "$out.sha256"
echo "== $out ($(du -h "$out" | cut -f1))"
