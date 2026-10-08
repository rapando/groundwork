#!/bin/sh
# Install groundwork: downloads the release for this OS/CPU, verifies its
# SHA-256 against the release's checksums.txt, and installs the binary.
#
#   curl -fsSL https://raw.githubusercontent.com/rapando/groundwork/main/install.sh | sh
#
# GROUNDWORK_VERSION       release to install, e.g. v0.1.0 or 0.1.0 (default: latest release)
# GROUNDWORK_INSTALL_DIR   where to put the binary (default: ~/.local/bin, or
#                          /usr/local/bin if writable and ~/.local/bin isn't on PATH)
# GROUNDWORK_DOWNLOAD_BASE mirror of the release downloads (testing, air-gapped installs)
set -eu

REPO="rapando/groundwork"

say() { printf '%s\n' "$*" >&2; }
die() { say "groundwork install: $*"; exit 1; }

fetch() { # url dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "need curl or wget"
  fi
}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS $(uname -s): groundwork runs on macOS and Linux" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m): groundwork is built for amd64 and arm64" ;;
esac

version="${GROUNDWORK_VERSION:-}"
if [ -z "$version" ]; then
  # the latest release's URL ends in its tag
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null || true)
  version="${url##*/}"
  case "$version" in v[0-9]*) ;; *) die "couldn't find the latest release; set GROUNDWORK_VERSION" ;; esac
fi
case "$version" in v*) ;; *) version="v$version" ;; esac
# releases are tagged vMAJOR.MINOR.PATCH (pre-releases: vX.Y.Z-rc.N)
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' ||
  die "GROUNDWORK_VERSION must be a release tag like v1.2.3 (got $version)"
base="${GROUNDWORK_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download/$version}"
archive="groundwork_${version#v}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t groundwork)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading groundwork ${version} for ${os}/${arch}..."
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$archive isn't listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $archive (expected $want, got $got): not installing"

tar -xzf "$tmp/$archive" -C "$tmp" groundwork || die "couldn't extract the binary"

on_path() { case ":$PATH:" in *":$1:"*) return 0 ;; esac; return 1; }
dir="${GROUNDWORK_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  dir="$HOME/.local/bin"
  if ! on_path "$dir" && [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  fi
fi
mkdir -p "$dir" || die "can't create $dir"
[ -w "$dir" ] || die "$dir isn't writable; set GROUNDWORK_INSTALL_DIR (this script never uses sudo)"
install -m 0755 "$tmp/groundwork" "$dir/groundwork.new" 2>/dev/null || { cp "$tmp/groundwork" "$dir/groundwork.new" && chmod 0755 "$dir/groundwork.new"; }
mv -f "$dir/groundwork.new" "$dir/groundwork"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$dir/groundwork" 2>/dev/null || true
fi

say "Installed $("$dir/groundwork" version 2>/dev/null || echo groundwork) to $dir/groundwork"
on_path "$dir" || say "Add $dir to your PATH:  export PATH=\"$dir:\$PATH\""
say "Next: cd into a repository and run  groundwork"
say "      (it starts the groundwork service; 'groundwork service install' starts it at login)"
if command -v pgrep >/dev/null 2>&1 && pgrep -x groundwork >/dev/null 2>&1; then
  say "A groundwork service is running the previous version: restart it with  groundwork service stop && groundwork service start"
fi
