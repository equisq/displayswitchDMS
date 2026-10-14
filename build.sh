#!/usr/bin/env bash
# Builds the Display Switch backend (bin/displayswitch-dmsd).
#
# CGO is disabled; the executable is still built for the current OS/architecture.
# Options:
#   --test         run the backend unit tests first
#   --yes          install wl-mirror (Duplicate mode) without asking, if missing
#   --no-mirror    never ask about / install wl-mirror
set -euo pipefail

run_tests=0
mirror_choice=ask
for arg in "$@"; do
    case "$arg" in
    --test) run_tests=1 ;;
    --yes | -y) mirror_choice=yes ;;
    --no-mirror) mirror_choice=no ;;
    -h | --help)
        sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
        exit 0
        ;;
    *)
        echo "unknown option: $arg" >&2
        exit 2
        ;;
    esac
done

cd "$(dirname "$0")/backend"

if ! command -v go >/dev/null 2>&1; then
    echo "error: Go 1.24 or newer is required to build the backend (https://go.dev/dl/)" >&2
    exit 1
fi

if [ "$run_tests" = 1 ]; then
    echo "running the backend tests..."
    go test ./...
fi

mkdir -p ../bin
flags=()
[ -d vendor ] && flags+=(-mod=vendor)
echo "building the Display Switch backend..."
CGO_ENABLED=0 go build "${flags[@]}" -buildvcs=false -trimpath -ldflags='-s -w' -o ../bin/displayswitch-dmsd .
echo "built: $(cd .. && pwd)/bin/displayswitch-dmsd"
# ---- optional: wl-mirror (needed only for the Duplicate mode) -------------------
# Installs it with the native package manager of the distribution. Failure here
# never fails the build: Duplicate is simply unavailable until it is installed.
install_wl_mirror() {
    local osr="${OS_RELEASE_FILE:-/etc/os-release}" ids="" sudo=""
    [ -r "$osr" ] && ids="$(. "$osr" 2>/dev/null; echo "${ID:-} ${ID_LIKE:-}")"
    if [ "$(id -u)" -ne 0 ]; then
        if command -v sudo >/dev/null 2>&1; then
            sudo="sudo"
        elif command -v doas >/dev/null 2>&1; then
            sudo="doas"
        else
            echo "wl-mirror: need root to install packages (sudo/doas not found)." >&2
            return 1
        fi
    fi
    has() { command -v "$1" >/dev/null 2>&1; }
    local id
    for id in $ids; do
        case "$id" in
        arch | manjaro | endeavouros | cachyos | garuda | artix) has pacman && { $sudo pacman -S --needed wl-mirror; return; } ;;
        debian | ubuntu | linuxmint | pop | neon | raspbian | zorin | elementary) has apt-get && { $sudo apt-get update && $sudo apt-get install -y wl-mirror; return; } ;;
        fedora | rhel | centos | nobara | bazzite) has dnf && { $sudo dnf install -y wl-mirror; return; } ;;
        opensuse* | suse | sles) has zypper && { $sudo zypper --non-interactive install wl-mirror; return; } ;;
        alpine | postmarketos) has apk && { $sudo apk add wl-mirror; return; } ;;
        void) has xbps-install && { $sudo xbps-install -S wl-mirror; return; } ;;
        gentoo) has emerge && { $sudo emerge --ask=n gui-apps/wl-mirror; return; } ;;
        nixos) has nix-env && { nix-env -iA nixos.wl-mirror; return; } ;;
        esac
    done
    # Unknown distribution: try whatever package manager exists.
    if has pacman; then $sudo pacman -S --needed wl-mirror; return; fi
    if has apt-get; then $sudo apt-get update && $sudo apt-get install -y wl-mirror; return; fi
    if has dnf; then $sudo dnf install -y wl-mirror; return; fi
    if has zypper; then $sudo zypper --non-interactive install wl-mirror; return; fi
    if has apk; then $sudo apk add wl-mirror; return; fi
    if has xbps-install; then $sudo xbps-install -S wl-mirror; return; fi
    echo "wl-mirror: no supported package manager found." >&2
    return 1
}

echo ""
if command -v wl-mirror >/dev/null 2>&1; then
    echo "wl-mirror is already installed: Duplicate mode is available."
elif [ "$mirror_choice" != no ]; then
    answer=n
    if [ "$mirror_choice" = yes ]; then
        answer=y
    elif [ -t 0 ]; then
        read -r -p "Do you want to install wl-mirror to get access to Duplicate mode? [y/N] " answer || answer=n
    else
        echo "wl-mirror is not installed (needed for Duplicate mode). Re-run with --yes to install it."
    fi
    case "$answer" in
    y | Y | yes | YES)
        if install_wl_mirror && command -v wl-mirror >/dev/null 2>&1; then
            echo "wl-mirror installed."
        else
            echo "Could not install wl-mirror automatically. Install it by hand" >&2
            echo "(https://github.com/Ferdi265/wl-mirror); Duplicate mode stays unavailable until then." >&2
        fi
        ;;
    esac
fi

echo ""
echo "Next steps:"
echo "1. In Settings → Plugins, click on Scan for Plugins and enable Display Switch."
echo "2. In Settings → Keyboard Shortcuts, add a shortcut (type: Shell Command):"
echo "   dms ipc call displayswitch toggle"
echo "3. Duplicate mode needs wl-mirror: re-run ./build.sh --yes to install it (skip if already done)."
echo "4. If a screen ever stays black, run: $(cd .. && pwd)/bin/displayswitch-dmsd rescue"
echo "5. Enjoy!"
