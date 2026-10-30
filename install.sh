#!/usr/bin/env bash
#
# pony installer: fetches a release of pony from github.com/LoremLabs/pony,
# checks it against the release's checksums.txt and puts it on your PATH.
#
# Usage:
#   curl -fsSL https://get.onepony.com/install.sh | bash
#   curl -fsSL https://get.onepony.com/install.sh | sudo bash   # install to /usr/local
#
# Env:
#   VERSION   "latest" (default), or "v0.1.0" / "0.1.0" for a given release.
#   PREFIX    install prefix; /usr/local as root, $HOME/.local otherwise.
#             The binary lands at $PREFIX/bin/pony.
#
# macOS (arm64, amd64) and Linux (arm64, amd64). Windows: download the zip
# from https://github.com/LoremLabs/pony/releases.

set -e

# Bump when the script changes: `curl -fsSL https://get.onepony.com/install.sh
# | grep INSTALLER_VERSION` shows which one is live.
INSTALLER_VERSION="1.0"

REPO_URL="https://github.com/LoremLabs/pony"
BIN="pony"

echo "Installing $BIN (installer v${INSTALLER_VERSION})"

# --- platform ----------------------------------------------------------------

case "$(uname -s 2>/dev/null || echo "")" in
  Darwin*) PLATFORM="darwin" ;;
  Linux*)  PLATFORM="linux" ;;
  *)
    echo "Error: this script supports macOS and Linux. On Windows, download pony from $REPO_URL/releases" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64)   ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *) echo "Error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

# --- downloader (curl or wget) -----------------------------------------------

# fetch <out> <url>: writes the body of <url> to <out>, fails on an HTTP error.
fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$2" -o "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$1" "$2"
  else
    echo "Error: neither curl nor wget found." >&2
    exit 1
  fi
}

# --- version -----------------------------------------------------------------

# Archive names carry the version (pony_<version>_<os>_<arch>.tar.gz), so a
# concrete tag is needed. For "latest", follow the /releases/latest redirect
# rather than the GitHub API, which rate-limits anonymous callers.
if [ "${VERSION:-latest}" = "latest" ]; then
  if command -v curl >/dev/null 2>&1; then
    EFFECTIVE_URL="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$REPO_URL/releases/latest")"
  elif command -v wget >/dev/null 2>&1; then
    EFFECTIVE_URL="$(wget -qSO /dev/null --max-redirect=10 "$REPO_URL/releases/latest" 2>&1 \
      | awk '/^  Location:/{u=$2} END{print u}')"
  else
    echo "Error: neither curl nor wget found." >&2
    exit 1
  fi
  TAG="${EFFECTIVE_URL##*/}"
  if [ -z "$TAG" ] || [ "$TAG" = "latest" ] || [ "$TAG" = "releases" ]; then
    echo "Error: could not find the latest release at $REPO_URL/releases/latest." >&2
    echo "       (set VERSION=vX.Y.Z to name one)" >&2
    exit 1
  fi
else
  case "$VERSION" in
    v*) TAG="$VERSION" ;;
    *)  TAG="v$VERSION" ;;
  esac
fi

NUMBER="${TAG#v}"
TARBALL="${BIN}_${NUMBER}_${PLATFORM}_${ARCH}.tar.gz"
DL="$REPO_URL/releases/download/$TAG"
echo "Release:     $TAG"
echo "Downloading: $DL/$TARBALL"

# --- download and verify -----------------------------------------------------

TMP_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TMP_DIR"' EXIT
for f in "$TARBALL" checksums.txt; do
  fetch "$TMP_DIR/$f" "$DL/$f" || {
    echo "Error: could not download $DL/$f" >&2
    echo "       (is $TAG a release with a build for ${PLATFORM}/${ARCH}? See $REPO_URL/releases)" >&2
    exit 1
  }
done

# Check our line only: checksums.txt lists every platform's archive.
if ! grep -q " $TARBALL\$" "$TMP_DIR/checksums.txt"; then
  echo "Error: $TARBALL is not listed in the release's checksums.txt." >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  SUM="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  SUM="shasum -a 256"
else
  SUM=""
fi
if [ -n "$SUM" ]; then
  (cd "$TMP_DIR" && grep " $TARBALL\$" checksums.txt | $SUM -c -) >/dev/null \
    || { echo "Error: checksum verification failed for $TARBALL." >&2; exit 1; }
  echo "Checksum verified."
else
  echo "Warning: no sha256sum or shasum found; the checksum was not verified."
fi

if ! tar -tzf "$TMP_DIR/$TARBALL" >/dev/null 2>&1; then
  echo "Error: the download is not a valid archive." >&2
  exit 1
fi

# --- install -----------------------------------------------------------------

if [ "$(id -u 2>/dev/null || echo 1)" -eq 0 ]; then
  PREFIX="${PREFIX:-/usr/local}"
else
  PREFIX="${PREFIX:-$HOME/.local}"
fi
INSTALL_DIR="$PREFIX/bin"
if ! mkdir -p "$INSTALL_DIR"; then
  echo "Error: could not create $INSTALL_DIR." >&2
  echo "       Use sudo, or set PREFIX to a directory you own:" >&2
  echo "         curl -fsSL https://get.onepony.com/install.sh | PREFIX=\$HOME/.local bash" >&2
  exit 1
fi

if [ -f "$INSTALL_DIR/$BIN" ]; then
  echo "Replacing $INSTALL_DIR/$BIN"
fi
# The archive also holds README.md and LICENSE; only the binary goes in bin/.
tar -xz -C "$INSTALL_DIR" -f "$TMP_DIR/$TARBALL" "$BIN"
chmod +x "$INSTALL_DIR/$BIN"
echo "Installed $INSTALL_DIR/$BIN"

# --- PATH --------------------------------------------------------------------

case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    echo
    echo "Done. Run \`$BIN help\` to get started."
    exit 0
    ;;
esac

echo
echo "$INSTALL_DIR is not on your PATH."

CURRENT_SHELL="$(basename "${SHELL:-/bin/sh}")"
case "$CURRENT_SHELL" in
  zsh)  RC_FILE="${ZDOTDIR:-$HOME}/.zprofile" ;;
  bash)
    if   [ -f "$HOME/.bash_profile" ]; then RC_FILE="$HOME/.bash_profile"
    elif [ -f "$HOME/.bash_login" ];   then RC_FILE="$HOME/.bash_login"
    else                                    RC_FILE="$HOME/.profile"
    fi
    ;;
  fish) RC_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/pony.fish" ;;
  *)    RC_FILE="$HOME/.profile" ;;
esac

PATH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
[ "$CURRENT_SHELL" = "fish" ] && PATH_LINE="fish_add_path \"$INSTALL_DIR\""

# Piped into bash, stdin is the script: ask on the terminal, if there is one.
if ( : </dev/tty >/dev/tty ) 2>/dev/null; then
  printf "Add it to %s? [y/N] " "$RC_FILE" >/dev/tty
  if read -r REPLY </dev/tty 2>/dev/null; then
    if [ "$REPLY" = "y" ] || [ "$REPLY" = "Y" ]; then
      mkdir -p "$(dirname "$RC_FILE")"
      echo "$PATH_LINE" >> "$RC_FILE"
      echo "Added to $RC_FILE. Open a new terminal, or run: source $RC_FILE"
    fi
  fi
else
  echo "To add it, append this line to $RC_FILE:"
  echo "  $PATH_LINE"
fi

echo
echo "Done. To start:"
echo "  $PATH_LINE && $BIN help"
