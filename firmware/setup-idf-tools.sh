#!/usr/bin/env bash
set -euo pipefail

firmware_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tools_dir="$firmware_dir/tools"
mkdir -p "$tools_dir/idf-prefix/bin"

if ! rustc +esp --version >/dev/null 2>&1; then
  printf '%s\n' 'Install the ESP Rust toolchain first: bash firmware/setup-toolchain.sh' >&2
  exit 1
fi

if [[ ! -x "$tools_dir/uv/uv" ]]; then
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) uv_target=x86_64-unknown-linux-gnu ;;
    Linux-aarch64) uv_target=aarch64-unknown-linux-gnu ;;
    *) printf '%s\n' 'This setup script currently supports Linux x86_64 and aarch64.' >&2; exit 1 ;;
  esac
  uv_archive=$(mktemp)
  trap 'rm -f "$uv_archive"' EXIT
  curl --fail --location --retry 3 \
    "https://github.com/astral-sh/uv/releases/download/0.13.7/uv-$uv_target.tar.gz" \
    --output "$uv_archive"
  mkdir -p "$tools_dir/uv"
  tar -xzf "$uv_archive" -C "$tools_dir/uv" --strip-components=1
fi

"$tools_dir/uv/uv" python install 3.12.16 \
  --install-dir "$tools_dir/python" --no-bin --no-cache

if [[ ! -x "$tools_dir/idf-prefix/bin/ldproxy" ]]; then
  cargo +stable install ldproxy --version 0.3.4 --locked --root "$tools_dir/idf-prefix"
fi

# ESP-IDF 5.3's Clang links to the legacy libxml2 SONAME. Recent Arch hosts
# supply a newer SONAME, so keep this compatibility library inside the project.
if [[ -f /etc/arch-release && "$(uname -m)" = x86_64 && ! -e "$tools_dir/idf-prefix/lib/libxml2.so.2" ]] \
  && ! (ldconfig -p | rg 'libxml2\.so\.2 ' >/dev/null); then
  compat_archive=$(mktemp)
  trap 'rm -f "${uv_archive:-}" "${compat_archive:-}"' EXIT
  curl --fail --location --retry 3 \
    'https://archive.archlinux.org/packages/l/libxml2-legacy/libxml2-legacy-2.13.9-2-x86_64.pkg.tar.zst' \
    --output "$compat_archive"
  printf '%s  %s\n' 'aac28a52e81422935e3bfa44044030640c5d525ad8f65a9672a3ab7139974652' "$compat_archive" \
    | sha256sum --check --status
  mkdir -p "$tools_dir/idf-prefix/lib"
  tar -xf "$compat_archive" -C "$tools_dir/idf-prefix/lib" --strip-components=2 \
    usr/lib/libxml2.so.2 usr/lib/libxml2.so.2.13.9
fi

printf '%s\n' 'ESP-IDF prerequisites ready. Run: bash firmware/build-idf-wifi.sh'
