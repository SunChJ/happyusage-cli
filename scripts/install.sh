#!/bin/sh
# happyusage installer
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/SunChJ/happyusage/main/scripts/install.sh | sh
#   VERSION=v0.1.0 BIN_DIR=/usr/local/bin sh install.sh

set -e

REPO="SunChJ/happyusage"
BIN_NAME="hu"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-}"

info() { printf "\033[1;34m==>\033[0m %s\n" "$1"; }
error() { printf "\033[1;31merror:\033[0m %s\n" "$1" >&2; exit 1; }

resolve_curl() {
  for candidate in "${HAPPYUSAGE_CURL:-}" /usr/local/opt/curl/bin/curl /usr/bin/curl "$(command -v curl 2>/dev/null || true)"; do
    [ -n "$candidate" ] || continue
    [ -x "$candidate" ] || continue
    "$candidate" --version >/dev/null 2>&1 && {
      printf '%s\n' "$candidate"
      return 0
    }
  done
  return 1
}

CURL_BIN="$(resolve_curl || true)"

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) error "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) error "unsupported architecture: $(uname -m)" ;;
  esac
}

fetch() {
  if [ -n "$CURL_BIN" ]; then
    "$CURL_BIN" -fsSL "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$1"
  else
    error "curl or wget is required"
  fi
}

download() {
  if [ -n "$CURL_BIN" ]; then
    "$CURL_BIN" -fsSL -o "$2" "$1"
  else
    wget -qO "$2" "$1"
  fi
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

info "Detected platform: ${OS}/${ARCH}"

# Resolve version
if [ -z "$VERSION" ]; then
  VERSION="$(fetch "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
fi

if [ -z "$VERSION" ]; then
  error "failed to determine latest version"
fi

info "Installing ${BIN_NAME} ${VERSION}"

# Build download URL
if [ "$OS" = "windows" ]; then
  ARCHIVE="${BIN_NAME}-${OS}-${ARCH}.zip"
else
  ARCHIVE="${BIN_NAME}-${OS}-${ARCH}.tar.gz"
fi

URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

# Download to temp dir
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

info "Downloading ${URL}"
download "$URL" "${TMPDIR}/${ARCHIVE}"

# Verify checksum
info "Verifying checksum"
download "$CHECKSUM_URL" "${TMPDIR}/checksums.txt"
EXPECTED="$(grep "${ARCHIVE}" "${TMPDIR}/checksums.txt" | awk '{print $1}')"
if [ -z "$EXPECTED" ]; then
  error "checksum not found for ${ARCHIVE}"
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL="$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
else
  error "sha256sum or shasum is required for checksum verification"
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  error "checksum mismatch: expected ${EXPECTED}, got ${ACTUAL}"
fi

# Extract
info "Extracting"
if [ "$OS" = "windows" ]; then
  unzip -q "${TMPDIR}/${ARCHIVE}" -d "${TMPDIR}"
else
  tar xzf "${TMPDIR}/${ARCHIVE}" -C "${TMPDIR}"
fi

# Install
mkdir -p "${BIN_DIR}"
mv "${TMPDIR}/${BIN_NAME}" "${BIN_DIR}/${BIN_NAME}"
chmod 755 "${BIN_DIR}/${BIN_NAME}"

info "Installed ${BIN_NAME} to ${BIN_DIR}/${BIN_NAME}"

# Check PATH
case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    printf "\n\033[1;33mwarning:\033[0m %s is not in your PATH.\n" "${BIN_DIR}"
    printf "Add this to your shell profile:\n\n  export PATH=\"%s:\$PATH\"\n\n" "${BIN_DIR}"
    ;;
esac

info "Run 'hu' to get started"
