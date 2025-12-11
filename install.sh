#!/usr/bin/env bash
#
# NTM Install Script
# https://github.com/Dicklesworthstone/ntm
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash
#
# Options:
#   --version=TAG   Install specific version (default: latest)
#   --dir=PATH      Install to custom directory (default: /usr/local/bin or ~/.local/bin)
#   --no-shell      Skip shell integration prompt
#
# The script will:
#   1. Detect your platform (OS and architecture)
#   2. Download the pre-compiled binary from GitHub releases
#   3. Install to a directory in your PATH
#   4. Optionally set up shell integration

set -euo pipefail

REPO_OWNER="Dicklesworthstone"
REPO_NAME="ntm"
BIN_NAME="ntm"

# Temp directory management
TMP_DIRS=()

cleanup_tmp_dirs() {
    local dir
    for dir in "${TMP_DIRS[@]:-}"; do
        [ -n "$dir" ] && rm -rf "$dir"
    done
}

make_tmp_dir() {
    local dir
    dir=$(mktemp -d)
    TMP_DIRS+=("$dir")
    printf '%s\n' "$dir"
}

trap cleanup_tmp_dirs EXIT

# Defaults
VERSION=""
INSTALL_DIR=""
NO_SHELL=false

# Parse arguments
for arg in "$@"; do
    case $arg in
        --version=*)
            VERSION="${arg#*=}"
            ;;
        --dir=*)
            INSTALL_DIR="${arg#*=}"
            ;;
        --no-shell)
            NO_SHELL=true
            ;;
        --help|-h)
            cat << 'EOF'
NTM Install Script

Usage: curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash

Options:
  --version=TAG   Install specific version (default: latest)
  --dir=PATH      Install to custom directory
  --no-shell      Skip shell integration prompt
  --help          Show this help

Examples:
  # Install latest version
  curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash

  # Install specific version
  curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash -s -- --version=v1.0.0

  # Install to custom directory
  curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash -s -- --dir=/opt/bin
EOF
            exit 0
            ;;
        *)
            echo "Unknown option: $arg"
            exit 1
            ;;
    esac
done

# Output helpers
print_info() { printf "\033[1;34m==>\033[0m %s\n" "$1"; }
print_success() { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
print_error() { printf "\033[1;31mError:\033[0m %s\n" "$1" >&2; }
print_warn() { printf "\033[1;33mWarning:\033[0m %s\n" "$1"; }

# Detect OS and architecture
detect_platform() {
    local os arch

    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        linux) os="linux" ;;
        darwin) os="darwin" ;;
        mingw*|msys*|cygwin*) os="windows" ;;
        freebsd) os="freebsd" ;;
        *) print_error "Unsupported OS: $os"; return 1 ;;
    esac

    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        arm64|aarch64) arch="arm64" ;;
        armv7*|armhf) arch="armv7" ;;
        *) print_error "Unsupported architecture: $arch"; return 1 ;;
    esac

    echo "${os}_${arch}"
}

# Get the best install directory
default_install_dir() {
    if [ -n "${INSTALL_DIR:-}" ]; then
        echo "$INSTALL_DIR"
        return
    fi

    # Prefer writable standard prefixes
    for dir in /usr/local/bin /opt/homebrew/bin /opt/local/bin; do
        if [ -d "$dir" ] && [ -w "$dir" ]; then
            echo "$dir"
            return
        fi
    done

    # Fall back to the first writable entry in PATH
    IFS=: read -r -a path_entries <<<"${PATH:-}"
    for dir in "${path_entries[@]}"; do
        if [ -d "$dir" ] && [ -w "$dir" ]; then
            echo "$dir"
            return
        fi
    done

    # Last resort: ~/.local/bin
    echo "${HOME}/.local/bin"
}

# Check if a command exists
has_cmd() {
    command -v "$1" >/dev/null 2>&1
}

# Download a file
download_file() {
    local url="$1"
    local dest="$2"

    if has_cmd curl; then
        curl -fsSL "$url" -o "$dest" || return 1
    elif has_cmd wget; then
        wget -q "$url" -O "$dest" || return 1
    else
        print_error "Neither curl nor wget found. Please install one."
        return 1
    fi
}

# Get the latest release info from GitHub
get_latest_release() {
    local url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest"

    if has_cmd curl; then
        curl -fsSL "$url" 2>/dev/null || return 1
    elif has_cmd wget; then
        wget -qO- "$url" 2>/dev/null || return 1
    else
        return 1
    fi
}

# Extract version from release JSON
extract_version() {
    # Simple grep/sed extraction - works without jq
    grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -1
}

# Find the download URL for a platform from release JSON
find_download_url() {
    local platform="$1"
    local release_json="$2"

    # Try to find the raw binary first (faster download)
    # Binary archive format: ntm_linux_amd64, ntm_darwin_arm64, etc.
    local binary_pattern="${BIN_NAME}_${platform}"

    # Extract browser_download_url for matching asset
    # Use || true to prevent grep from causing script exit when no match found
    echo "$release_json" | grep -o '"browser_download_url":[[:space:]]*"[^"]*'"${binary_pattern}"'"' | \
        sed -E 's/.*"browser_download_url":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true
}

# Ensure install directory exists
ensure_install_dir() {
    local dir="$1"

    if [ -d "$dir" ]; then
        return 0
    fi

    if mkdir -p "$dir" 2>/dev/null; then
        return 0
    fi

    print_info "Creating $dir requires sudo..."
    sudo mkdir -p "$dir"
}

# Main installation function
install_ntm() {
    local platform version install_dir tmp_dir download_url binary_path

    print_info "Installing ${BIN_NAME}..."

    # Detect platform
    platform=$(detect_platform) || exit 1
    print_info "Detected platform: $platform"

    # Determine install directory
    install_dir=$(default_install_dir)
    print_info "Install directory: $install_dir"

    # Get version
    if [ -z "$VERSION" ]; then
        print_info "Fetching latest version..."
        local release_json
        release_json=$(get_latest_release) || {
            print_error "Could not fetch release info from GitHub"
            exit 1
        }
        version=$(echo "$release_json" | extract_version)
        if [ -z "$version" ]; then
            print_error "Could not determine latest version"
            exit 1
        fi
    else
        version="$VERSION"
        # Fetch release info for this version
        local release_json
        release_json=$(get_latest_release) || {
            print_error "Could not fetch release info from GitHub"
            exit 1
        }
    fi

    print_info "Installing ${BIN_NAME} ${version} for ${platform}"

    # Fetch release info if we don't have it
    if [ -z "${release_json:-}" ]; then
        release_json=$(get_latest_release) || {
            print_error "Could not fetch release info from GitHub"
            exit 1
        }
    fi

    # Find download URL
    download_url=$(find_download_url "$platform" "$release_json")

    if [ -z "$download_url" ]; then
        # Try alternate naming conventions
        # Some releases use dashes instead of underscores
        local alt_platform="${platform//_/-}"
        download_url=$(find_download_url "$alt_platform" "$release_json")
    fi

    if [ -z "$download_url" ]; then
        # macOS uses universal binary (darwin_all) instead of arch-specific
        case "$platform" in
            darwin_amd64|darwin_arm64)
                download_url=$(find_download_url "darwin_all" "$release_json")
                ;;
        esac
    fi

    if [ -z "$download_url" ]; then
        print_error "No pre-built binary found for $platform"
        print_info "You can build from source with: go install github.com/${REPO_OWNER}/${REPO_NAME}/cmd/${BIN_NAME}@latest"
        exit 1
    fi

    print_info "Downloading from $download_url..."

    # Create temp directory
    tmp_dir=$(make_tmp_dir)
    binary_path="${tmp_dir}/${BIN_NAME}"

    # Download
    if ! download_file "$download_url" "$binary_path"; then
        print_error "Download failed"
        exit 1
    fi

    # Make executable
    chmod +x "$binary_path"

    # Verify it runs
    if ! "$binary_path" version >/dev/null 2>&1; then
        print_error "Downloaded binary failed verification"
        exit 1
    fi

    # Install
    ensure_install_dir "$install_dir"
    local dest_path="${install_dir}/${BIN_NAME}"

    if [ -w "$install_dir" ]; then
        mv "$binary_path" "$dest_path"
    else
        print_info "Installing to $install_dir requires sudo..."
        sudo mv "$binary_path" "$dest_path"
    fi

    print_success "Installed ${BIN_NAME} ${version} to ${dest_path}"

    # Check PATH
    if ! echo "$PATH" | grep -q "$install_dir"; then
        print_warn "${install_dir} is not in your PATH"
        echo ""
        echo "Add to your shell rc file:"
        echo "  export PATH=\"\$PATH:${install_dir}\""
        echo ""
    fi

    # Shell integration
    if [ "$NO_SHELL" != true ]; then
        setup_shell_integration
    fi

    echo ""
    print_success "Installation complete!"
    echo ""
    echo "Quick start:"
    echo "  ntm spawn myproject --cc=2 --cod=2   # Create session with agents"
    echo "  ntm attach myproject                  # Attach to session"
    echo "  ntm palette                           # Open command palette"
    echo "  ntm tutorial                          # Interactive tutorial"
    echo ""
    echo "Run 'ntm --help' for full documentation."
}

# Setup shell integration
setup_shell_integration() {
    local shell_name rc_file init_cmd

    # Detect shell
    shell_name=$(basename "${SHELL:-bash}")

    case "$shell_name" in
        zsh)
            rc_file="${HOME}/.zshrc"
            init_cmd='eval "$(ntm init zsh)"'
            ;;
        bash)
            rc_file="${HOME}/.bashrc"
            init_cmd='eval "$(ntm init bash)"'
            ;;
        fish)
            rc_file="${HOME}/.config/fish/config.fish"
            init_cmd='ntm init fish | source'
            ;;
        *)
            return
            ;;
    esac

    # Check if already configured
    if [ -f "$rc_file" ] && grep -q "ntm init" "$rc_file"; then
        print_info "Shell integration already configured in ${rc_file}"
        return
    fi

    echo ""
    echo "Shell Integration"
    echo ""
    echo "Add this to ${rc_file}:"
    echo "  ${init_cmd}"
    echo ""

    # Only prompt if interactive
    if [ -t 0 ] && [ -t 1 ]; then
        printf "Add it now? [y/N]: "
        read -r answer
        case "$answer" in
            y|Y|yes|YES)
                echo "" >> "$rc_file"
                echo "# NTM - Named Tmux Manager" >> "$rc_file"
                echo "$init_cmd" >> "$rc_file"
                print_success "Added to ${rc_file}"
                echo ""
                echo "Run 'source ${rc_file}' or restart your shell to activate."
                ;;
            *)
                echo "Skipped. Add it manually when ready."
                ;;
        esac
    fi
}

# Run installation
install_ntm
