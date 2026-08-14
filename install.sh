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
#   --easy-mode     Non-interactive: auto-configure shell integration and PATH
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
        # Use if statement to avoid set -e triggering on empty dir check
        if [ -n "$dir" ]; then
            rm -rf "$dir"
        fi
    done
}

trap cleanup_tmp_dirs EXIT

# Defaults
VERSION=""
INSTALL_DIR=""
NO_SHELL=false
EASY_MODE=false

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
        --easy-mode)
            EASY_MODE=true
            ;;
        --help|-h)
            cat << 'EOF'
NTM Install Script

Usage: curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash

Options:
  --version=TAG   Install specific version (default: latest)
  --dir=PATH      Install to custom directory
  --no-shell      Skip shell integration prompt
  --easy-mode     Non-interactive: auto-configure shell integration and PATH
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

is_release_version() {
    local version="${1#v}"
    [[ "$version" =~ ^[0-9]+[.][0-9]+[.][0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?([+][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]
}

normalize_release_version() {
    local version="$1"

    if ! is_release_version "$version"; then
        print_error "Invalid version: $version (expected vX.Y.Z or X.Y.Z)"
        return 1
    fi

    printf 'v%s\n' "${version#v}"
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

# Probe-download a file that is allowed to be missing. Identical to
# download_file except the transport tool's own stderr is suppressed, so an
# expected 404 on one candidate does not print a scary error while a later
# candidate goes on to succeed. Real failures are still reported via the
# non-zero return code, which callers must handle.
try_download_file() {
    local url="$1"
    local dest="$2"

    if has_cmd curl; then
        curl -fsSL "$url" -o "$dest" 2>/dev/null || return 1
    elif has_cmd wget; then
        wget -q "$url" -O "$dest" 2>/dev/null || return 1
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

resolve_latest_version_from_redirect() {
    local url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest"
    local effective_url tag

    if has_cmd curl; then
        effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$url" 2>/dev/null) || return 1
    elif has_cmd wget; then
        effective_url=$(
            wget -S --spider "$url" 2>&1 |
                awk 'tolower($1) == "location:" { loc = $2 } END { sub(/\r$/, "", loc); print loc }'
        ) || return 1
        [[ -n "$effective_url" ]] || return 1
    else
        return 1
    fi

    tag="${effective_url##*/}"
    if is_release_version "$tag"; then
        normalize_release_version "$tag"
        return 0
    fi

    return 1
}

# Get a specific tagged release info from GitHub
get_release_by_tag() {
    local version="$1"
    local url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/tags/${version}"

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

# Find the best asset name for a platform from release JSON
find_asset_name() {
    local platform="$1"
    local release_json="$2"
    local asset_name

    if [[ "$platform" == windows_* ]]; then
        asset_name=$(printf '%s\n' "$release_json" | grep -o "\"name\":[[:space:]]*\"${BIN_NAME}_${platform}\\.zip\"" | \
            sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true)
        if [ -n "$asset_name" ]; then
            printf '%s\n' "$asset_name"
            return 0
        fi
    fi

    asset_name=$(printf '%s\n' "$release_json" | grep -o "\"name\":[[:space:]]*\"${BIN_NAME}_[^\"]*_${platform}\\.tar\\.gz\"" | \
        sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true)
    if [ -n "$asset_name" ]; then
        printf '%s\n' "$asset_name"
        return 0
    fi

    asset_name=$(printf '%s\n' "$release_json" | grep -o "\"name\":[[:space:]]*\"${BIN_NAME}_${platform}\\.tar\\.gz\"" | \
        sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true)
    if [ -n "$asset_name" ]; then
        printf '%s\n' "$asset_name"
        return 0
    fi

    asset_name=$(printf '%s\n' "$release_json" | grep -o "\"name\":[[:space:]]*\"${BIN_NAME}_[^\"]*_${platform}\\.zip\"" | \
        sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true)
    if [ -n "$asset_name" ]; then
        printf '%s\n' "$asset_name"
        return 0
    fi

    asset_name=$(printf '%s\n' "$release_json" | grep -o "\"name\":[[:space:]]*\"${BIN_NAME}_${platform}\\.zip\"" | \
        sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true)
    if [ -n "$asset_name" ]; then
        printf '%s\n' "$asset_name"
        return 0
    fi

    asset_name=$(printf '%s\n' "$release_json" | grep -o "\"name\":[[:space:]]*\"${BIN_NAME}_${platform}\"" | \
        sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/' | head -1 || true)
    printf '%s\n' "$asset_name"
}

build_download_url() {
    local version="$1"
    local asset_name="$2"
    printf 'https://github.com/%s/%s/releases/download/%s/%s\n' "$REPO_OWNER" "$REPO_NAME" "$version" "$asset_name"
}

download_release_checksums() {
    local version="$1"
    local asset_name="$2"
    local dest_dir="$3"
    local candidate checksum_url checksum_path
    # SHA256SUMS is what current releases actually publish, so probe it first;
    # the other names are legacy/per-asset fallbacks that usually 404.
    local -a candidates=(
        "SHA256SUMS"
        "checksums.txt"
        "${asset_name}.sha256"
    )

    # Only transport failures fall through to the next release asset. Once a
    # checksum document downloads, verification treats it as authoritative and
    # fails closed rather than accepting a different checksum source.
    for candidate in "${candidates[@]}"; do
        checksum_url=$(build_download_url "$version" "$candidate")
        checksum_path="${dest_dir}/${candidate}"
        if try_download_file "$checksum_url" "$checksum_path"; then
            printf '%s\n' "$checksum_path"
            return 0
        fi
    done

    return 1
}

verify_downloaded_asset() {
    local asset_path="$1"
    local checksum_path="$2"
    local asset_name="$3"
    local expected actual

    if [ ! -f "$checksum_path" ]; then
        print_error "Checksums file not found: $checksum_path"
        return 1
    fi

    expected=$(
        awk -v name="$asset_name" '
            {
                filename = $NF
                sub(/\r$/, "", filename)
                if (filename == name) {
                    print $1
                    exit
                }
            }
        ' "$checksum_path"
    )

    if ! printf '%s\n' "$expected" | grep -Eq '^[0-9A-Fa-f]{64}$'; then
        print_error "No SHA256 checksum found for $asset_name"
        return 1
    fi

    if has_cmd sha256sum; then
        actual=$(sha256sum "$asset_path" | awk '{print $1}')
    elif has_cmd shasum; then
        actual=$(shasum -a 256 "$asset_path" | awk '{print $1}')
    else
        print_error "Need sha256sum or shasum to verify downloaded asset"
        return 1
    fi

    expected=$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')
    actual=$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')

    if [ "$expected" != "$actual" ]; then
        print_error "Checksum mismatch for $asset_name"
        return 1
    fi
}

asset_url_exists() {
    local url="$1"

    if has_cmd curl; then
        curl -fsIL -o /dev/null "$url" 2>/dev/null
        return $?
    elif has_cmd wget; then
        wget -q --spider "$url" 2>/dev/null
        return $?
    fi

    return 1
}

find_downloadable_asset_name() {
    local platform="$1"
    local version="$2"
    local clean_version="${version#v}"
    local alt_platform="${platform//_/-}"
    local candidate url
    local -a candidates=(
        "${BIN_NAME}_${clean_version}_${platform}.tar.gz"
        "${BIN_NAME}_${version}_${platform}.tar.gz"
        "${BIN_NAME}_${platform}.tar.gz"
        "${BIN_NAME}-${clean_version}-${alt_platform}.tar.gz"
        "${BIN_NAME}_${clean_version}_${platform}.zip"
        "${BIN_NAME}_${version}_${platform}.zip"
        "${BIN_NAME}_${platform}.zip"
    )

    for candidate in "${candidates[@]}"; do
        url=$(build_download_url "$version" "$candidate")
        if asset_url_exists "$url"; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done

    return 1
}

extract_downloaded_asset() {
    local asset_name="$1"
    local downloaded_path="$2"
    local tmp_dir="$3"

    case "$asset_name" in
        *.tar.gz)
            tar -xzf "$downloaded_path" -C "$tmp_dir"
            ;;
        *.zip)
            if has_cmd unzip; then
                unzip -q "$downloaded_path" -d "$tmp_dir"
            elif has_cmd bsdtar; then
                bsdtar -xf "$downloaded_path" -C "$tmp_dir"
            else
                print_error "Need unzip or bsdtar to extract $asset_name"
                return 1
            fi
            ;;
        *)
            return 0
            ;;
    esac

    return 0
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
    local platform version install_dir tmp_dir asset_name download_url download_path binary_path checksum_path

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
        version=$(resolve_latest_version_from_redirect || true)
        if [ -n "$version" ]; then
            release_json=""
        else
            release_json=$(get_latest_release) || {
                print_error "Could not fetch release info from GitHub"
                exit 1
            }
            version=$(echo "$release_json" | extract_version)
        fi
        if [ -z "$version" ]; then
            print_error "Could not determine latest version"
            exit 1
        fi
    else
        version="$VERSION"
    fi

    if ! version=$(normalize_release_version "$version"); then
        exit 1
    fi

    if [ -n "$VERSION" ]; then
        # Fetch release info for this version
        local release_json
        release_json=$(get_release_by_tag "$version" || true)
        if [ -z "$release_json" ]; then
            print_warn "Could not fetch release info for ${version} from GitHub; trying known asset names"
        fi
    fi

    print_info "Installing ${BIN_NAME} ${version} for ${platform}"

    # Fetch release info if we don't have it. If the API is rate-limited, fall
    # back to probing the deterministic release asset names below.
    if [ -z "${release_json:-}" ]; then
        release_json=$(get_release_by_tag "$version" || true)
    fi

    # Find download asset
    asset_name=$(find_asset_name "$platform" "$release_json")

    if [ -z "$asset_name" ]; then
        # Try alternate naming conventions
        # Some releases use dashes instead of underscores
        local alt_platform="${platform//_/-}"
        asset_name=$(find_asset_name "$alt_platform" "$release_json")
    fi

    if [ -z "$asset_name" ]; then
        # macOS uses universal binary (darwin_all) instead of arch-specific
        case "$platform" in
            darwin_amd64|darwin_arm64)
                asset_name=$(find_asset_name "darwin_all" "$release_json")
                if [ -z "$asset_name" ]; then
                    asset_name=$(find_downloadable_asset_name "darwin_all" "$version" || true)
                fi
                ;;
        esac
    fi

    if [ -z "$asset_name" ]; then
        asset_name=$(find_downloadable_asset_name "$platform" "$version" || true)
    fi

    if [ -z "$asset_name" ]; then
        print_error "No pre-built binary found for $platform"
        print_info "You can build this version from source with: git clone --branch ${version} --depth 1 https://github.com/${REPO_OWNER}/${REPO_NAME}.git && cd ${REPO_NAME} && go install ./cmd/${BIN_NAME}"
        exit 1
    fi

    download_url=$(build_download_url "$version" "$asset_name")

    print_info "Downloading from $download_url..."

    # Create temp directory
    tmp_dir=$(mktemp -d)
    TMP_DIRS+=("$tmp_dir")
    download_path="${tmp_dir}/${asset_name}"
    binary_path="${tmp_dir}/${BIN_NAME}"

    # Download
    if ! download_file "$download_url" "$download_path"; then
        print_error "Download failed"
        exit 1
    fi

    print_info "Verifying checksum..."
    if ! checksum_path=$(download_release_checksums "$version" "$asset_name" "$tmp_dir"); then
        print_error "Could not download release checksums"
        exit 1
    fi
    if ! verify_downloaded_asset "$download_path" "$checksum_path" "$asset_name"; then
        exit 1
    fi

    if ! extract_downloaded_asset "$asset_name" "$download_path" "$tmp_dir"; then
        print_error "Could not extract downloaded archive"
        exit 1
    fi

    if [ ! -f "$binary_path" ] && [ -f "${tmp_dir}/${BIN_NAME}.exe" ]; then
        binary_path="${tmp_dir}/${BIN_NAME}.exe"
    fi

    if [ ! -f "$binary_path" ]; then
        print_error "Could not find ${BIN_NAME} in downloaded asset"
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
        if [ "$EASY_MODE" = true ]; then
            # Auto-add to PATH in easy-mode
            local path_updated=0
            for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
                if [ -e "$rc" ] && [ -w "$rc" ]; then
                    if ! grep -F "$install_dir" "$rc" >/dev/null 2>&1; then
                        {
                            echo ""
                            echo "# Added by ntm installer"
                            echo "export PATH=\"\$PATH:${install_dir}\""
                        } >> "$rc"
                        path_updated=1
                    fi
                fi
            done
            if [ "$path_updated" -eq 1 ]; then
                print_info "PATH updated in shell rc files; restart shell to use ntm"
            fi
        else
            print_warn "${install_dir} is not in your PATH"
            echo ""
            echo "Add to your shell rc file:"
            echo "  export PATH=\"\$PATH:${install_dir}\""
            echo ""
        fi
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
    echo "Tip: You can also install via Homebrew:"
    echo "  brew install dicklesworthstone/tap/ntm"
    echo ""
    echo "Run 'ntm --help' for full documentation."
}

# Get the shell integration command for the installed version
# v1.5.0 and earlier use "ntm init <shell>", v1.6.0+ use "ntm shell <shell>"
get_shell_cmd() {
    local shell_name="$1"
    local installed_version="$2"

    # Determine which command to use based on version
    # v1.5.0 and earlier use "ntm init", v1.6.0+ use "ntm shell"
    local use_init=false
    if [ -n "$installed_version" ]; then
        # Strip 'v' prefix for comparison
        local ver="${installed_version#v}"
        local major minor
        major=$(echo "$ver" | cut -d. -f1)
        minor=$(echo "$ver" | cut -d. -f2)
        # Use "ntm init" for v1.5.x and earlier
        if [ "$major" -eq 1 ] && [ "$minor" -le 5 ] 2>/dev/null; then
            use_init=true
        fi
    fi

    local cmd_name="shell"
    if [ "$use_init" = true ]; then
        cmd_name="init"
    fi

    case "$shell_name" in
        fish)
            echo "ntm $cmd_name fish | source"
            ;;
        *)
            echo "eval \"\$(ntm $cmd_name $shell_name)\""
            ;;
    esac
}

# Update legacy "ntm init" entries to "ntm shell" in a shell rc file
upgrade_shell_integration() {
    local rc_file="$1"

    if [ ! -f "$rc_file" ]; then
        return 1
    fi

    # Check if the file has legacy "ntm init" entries
    if ! grep -q 'ntm init' "$rc_file"; then
        return 1
    fi

    # Create a backup
    local backup_file="${rc_file}.ntm-backup"
    cp "$rc_file" "$backup_file"

    # Replace "ntm init" with "ntm shell" in the file
    # Handle both eval "$(ntm init bash)" and ntm init fish | source patterns
    if sed -i.bak 's/ntm init/ntm shell/g' "$rc_file" 2>/dev/null; then
        rm -f "${rc_file}.bak"
        print_success "Updated shell integration in ${rc_file}"
        print_info "Backup saved to ${backup_file}"
        return 0
    else
        # macOS sed requires different syntax
        if sed -i '' 's/ntm init/ntm shell/g' "$rc_file" 2>/dev/null; then
            print_success "Updated shell integration in ${rc_file}"
            print_info "Backup saved to ${backup_file}"
            return 0
        fi
    fi

    # Restore from backup if sed failed
    mv "$backup_file" "$rc_file"
    return 1
}

# Setup shell integration
setup_shell_integration() {
    local shell_name rc_file init_cmd

    # Detect shell
    shell_name=$(basename "${SHELL:-bash}")

    case "$shell_name" in
        zsh)
            rc_file="${HOME}/.zshrc"
            ;;
        bash)
            rc_file="${HOME}/.bashrc"
            ;;
        fish)
            rc_file="${HOME}/.config/fish/config.fish"
            ;;
        *)
            return
            ;;
    esac

    # Get the appropriate shell command for the installed version
    init_cmd=$(get_shell_cmd "$shell_name" "$VERSION")

    # Check if already configured with current "ntm shell" command
    if [ -f "$rc_file" ] && grep -q "ntm shell" "$rc_file"; then
        print_info "Shell integration already configured in ${rc_file}"
        return
    fi

    # Check for legacy "ntm init" entries that need upgrading
    if [ -f "$rc_file" ] && grep -q "ntm init" "$rc_file"; then
        print_warn "Legacy shell integration detected in ${rc_file}"
        print_info "The 'ntm init' command was renamed to 'ntm shell' in v1.6.0."

        # In easy-mode, auto-upgrade
        if [ "$EASY_MODE" = true ]; then
            if upgrade_shell_integration "$rc_file"; then
                print_info "Restart your shell or run 'source ${rc_file}' to activate."
            else
                print_warn "Could not auto-update. Please manually replace 'ntm init' with 'ntm shell'."
            fi
            return
        fi

        # Only prompt if interactive
        if [ -t 0 ] && [ -t 1 ]; then
            printf "Update to 'ntm shell' automatically? [Y/n]: "
            read -r answer
            case "$answer" in
                n|N|no|NO)
                    print_info "Please manually replace 'ntm init' with 'ntm shell' in ${rc_file}"
                    ;;
                *)
                    if upgrade_shell_integration "$rc_file"; then
                        print_info "Restart your shell or run 'source ${rc_file}' to activate."
                    else
                        print_warn "Could not auto-update. Please manually replace 'ntm init' with 'ntm shell'."
                    fi
                    ;;
            esac
        else
            print_info "Run installer with --easy-mode to auto-update, or manually replace 'ntm init' with 'ntm shell'."
        fi
        return
    fi

    echo ""
    echo "Shell Integration"
    echo ""
    echo "Add this to ${rc_file}:"
    echo "  ${init_cmd}"
    echo ""

    # In easy-mode, auto-add without prompting
    if [ "$EASY_MODE" = true ]; then
        {
            echo ""
            echo "# NTM - Named Tmux Manager"
            echo "$init_cmd"
        } >> "$rc_file"
        print_success "Added shell integration to ${rc_file}"
        print_info "Restart your shell or run 'source ${rc_file}' to activate."
        return
    fi

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
