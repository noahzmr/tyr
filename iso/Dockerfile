# Build environment for the Tyr live ISO. Runs for the target architecture
# (linux/amd64 or linux/arm64): the signed shim and GRUB come from it.
FROM debian:trixie

RUN printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie trixie-updates\nComponents: main non-free-firmware\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://deb.debian.org/debian-security\nSuites: trixie-security\nComponents: main non-free-firmware\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
        > /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && arch=$(dpkg --print-architecture) \
    && case "$arch" in \
         amd64) boot="grub-pc-bin grub-efi-amd64-bin grub-efi-amd64-signed shim-signed" ;; \
         arm64) boot="grub-efi-arm64-bin grub-efi-arm64-signed shim-signed" ;; \
       esac \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
         mmdebstrap squashfs-tools xorriso mtools dosfstools rpm2cpio cpio \
         ca-certificates $boot \
    && rm -rf /var/lib/apt/lists/*
