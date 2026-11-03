#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${1:-${repo_root}/dist}"

if [[ ! -d "$dist_dir" ]]; then
  echo "release artifact directory does not exist: $dist_dir" >&2
  exit 1
fi

shopt -s nullglob
archives=("$dist_dir"/aidpfs_linux_*.tar.gz)
packages=("$dist_dir"/aidpfs_*_amd64.deb "$dist_dir"/aidpfs_*_arm64.deb)
checksums=("$dist_dir"/SHA256SUMS)
if ((${#archives[@]} != 2 || ${#packages[@]} != 2 || ${#checksums[@]} != 1)); then
  echo "expected 2 Linux archives, 2 Debian packages, and one SHA256SUMS file" >&2
  exit 1
fi

(cd "$dist_dir" && sha256sum --check SHA256SUMS)

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

for archive in "${archives[@]}"; do
  arch="${archive##*_linux_}"
  arch="${arch%.tar.gz}"
  if [[ "$arch" != amd64 && "$arch" != arm64 ]]; then
    echo "unexpected archive architecture: $archive" >&2
    exit 1
  fi
  archive_dir="$tmp_dir/archive-$arch"
  mkdir -p "$archive_dir"
  tar -xzf "$archive" -C "$archive_dir"
  for file in aidpfs README.md LICENSE THIRD_PARTY_NOTICES.md; do
    if [[ ! -s "$archive_dir/$file" ]]; then
      echo "$archive is missing $file" >&2
      exit 1
    fi
  done
  if [[ "$arch" == amd64 && "$(uname -m)" == x86_64 ]]; then
    "$archive_dir/aidpfs" --version
  fi
done

for package in "${packages[@]}"; do
  arch="${package##*_}"
  arch="${arch%.deb}"
  package_arch="$(dpkg-deb --field "$package" Architecture)"
  package_name="$(dpkg-deb --field "$package" Package)"
  package_depends="$(dpkg-deb --field "$package" Depends)"
  if [[ "$package_name" != aidpfs || "$package_arch" != "$arch" || "$package_depends" != *fuse3* ]]; then
    echo "unexpected Debian package metadata: $package" >&2
    exit 1
  fi
  package_dir="$tmp_dir/package-$arch"
  mkdir -p "$package_dir"
  dpkg-deb --extract "$package" "$package_dir"
  for file in \
    "$package_dir/usr/bin/aidpfs" \
    "$package_dir/usr/share/doc/aidpfs/README.md" \
    "$package_dir/usr/share/doc/aidpfs/LICENSE" \
    "$package_dir/usr/share/doc/aidpfs/THIRD_PARTY_NOTICES.md"; do
    if [[ ! -s "$file" ]]; then
      echo "$package is missing ${file#"$package_dir"/}" >&2
      exit 1
    fi
  done
done

echo "Validated Linux amd64/arm64 archives, Debian packages, metadata, documentation, and checksums."
