#!/bin/bash
# Installs the NDI runtime library (libndi.so.6) from the official NDI SDK for
# Linux, so the controller finds it on the system library path.
#
#   scripts/install-ndi-sdk.sh            # install to /usr/local/lib (uses sudo)
#   PREFIX=/opt/ndi scripts/install-ndi-sdk.sh
#
# The SDK installer shows the NDI SDK license and asks you to accept it. Set
# NDI_ACCEPT_LICENSE=yes only if you have read and accepted it already (e.g.
# when provisioning more machines).
#
# Based on DistroAV's CI/libndi-get.sh. Unlike that script it registers the
# library directory with the dynamic linker when needed: Fedora/RHEL do not
# search /usr/local/lib by default.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
LIBDIR="$PREFIX/lib"
INSTALLER_NAME="Install_NDI_SDK_v6_Linux"
INSTALLER_URL="https://downloads.ndi.tv/SDK/NDI_SDK_Linux/$INSTALLER_NAME.tar.gz"
ARCH_DIR="x86_64-linux-gnu"

if [[ "$(uname -m)" != "x86_64" ]]; then
    echo "This script installs the x86_64 library only (this machine: $(uname -m))." >&2
    exit 1
fi

SUDO=""
if [[ $EUID -ne 0 ]]; then
    SUDO="sudo"
fi

TMP="$(mktemp -d --tmpdir ndisdk.XXXXXXX)"
trap 'rm -rf "$TMP"' EXIT
cd "$TMP"

echo "Downloading $INSTALLER_URL ..."
curl -fL --retry 5 -o "$INSTALLER_NAME.tar.gz" "$INSTALLER_URL"
tar -xzf "$INSTALLER_NAME.tar.gz"

# The installer pages through the license and extracts "NDI SDK for Linux/"
# into the current directory once it is accepted.
if [[ "${NDI_ACCEPT_LICENSE:-}" == "yes" ]]; then
    yes | PAGER=cat sh "$INSTALLER_NAME.sh" >/dev/null
else
    PAGER="${PAGER:-less}" sh "$INSTALLER_NAME.sh"
fi

SRC="$TMP/NDI SDK for Linux/lib/$ARCH_DIR"
if ! ls "$SRC"/libndi.so.6* >/dev/null 2>&1; then
    echo "libndi.so.6 not found in the SDK (license not accepted?)" >&2
    exit 1
fi

echo "Installing libndi to $LIBDIR ..."
$SUDO mkdir -p "$LIBDIR"
$SUDO cp -P "$SRC"/libndi.so* "$LIBDIR"/

# Make sure the dynamic linker searches $LIBDIR.
if ! ldconfig -v 2>/dev/null | grep -q "^$LIBDIR:"; then
    echo "Adding $LIBDIR to /etc/ld.so.conf.d/ndi-sdk.conf"
    echo "$LIBDIR" | $SUDO tee /etc/ld.so.conf.d/ndi-sdk.conf >/dev/null
fi
$SUDO ldconfig

if ldconfig -p | grep -q 'libndi\.so\.6 '; then
    echo "Installed: $(ldconfig -p | grep 'libndi\.so\.6 ' | head -1 | sed 's/^[[:space:]]*//')"
    strings "$LIBDIR/libndi.so.6" | grep -m1 '^NDI SDK LINUX' || true
else
    echo "libndi.so.6 was copied to $LIBDIR but the dynamic linker does not find it." >&2
    exit 1
fi
