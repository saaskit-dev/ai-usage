#!/bin/bash
set -e

REPO="saaskit-dev/ai-usage"
BINARY_NAME="ai-usage"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="$HOME/.config/ai-usage"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Linux*)     echo "linux";;
        Darwin*)    echo "macos";;
        *)          echo "unknown";;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64)     echo "amd64";;
        arm64|aarch64) echo "arm64";;
        *)          echo "amd64";;
    esac
}

# Get latest release version
get_latest_version() {
    curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep -o '"tag_name": "*[^"]*"' | cut -d'"' -f4
}

# Download and install binary
install_binary() {
    local version=$1
    local os=$(detect_os)
    local arch=$(detect_arch)
    local filename="${BINARY_NAME}-${os}-${arch}"
    local download_url="https://github.com/${REPO}/releases/download/${version}/${filename}.tar.gz"
    
    log_info "Downloading ${filename}..."
    
    # Create temp directory
    local tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT
    
    # Download
    curl -sL "$download_url" -o "${tmp_dir}/${filename}.tar.gz"
    tar -xzf "${tmp_dir}/${filename}.tar.gz" -C "$tmp_dir"
    
    # Install binary
    log_info "Installing to ${INSTALL_DIR}..."
    sudo cp "${tmp_dir}/${filename}/${BINARY_NAME}" "${INSTALL_DIR}/"
    sudo chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    
    # Create config directory
    mkdir -p "$CONFIG_DIR"
    
    log_info "Installed successfully!"
}

# Install via Homebrew
install_via_homebrew() {
    log_info "Installing via Homebrew..."
    
    # Check if formula exists locally
    if [ -f "./homebrew/ai-usage.rb" ]; then
        brew install ./homebrew/ai-usage.rb
    else
        # Tap (if available)
        brew install ai-usage
    fi
    
    log_info "Installed via Homebrew!"
}

# Main
main() {
    local install_method=""
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            --homebrew)
                install_method="homebrew"
                shift
                ;;
            --version)
                VERSION="$2"
                shift 2
                ;;
            --help|-h)
                echo "Usage: $0 [OPTIONS]"
                echo ""
                echo "Options:"
                echo "  --homebrew    Install via Homebrew"
                echo "  --version     Specify version to install (default: latest)"
                echo "  --help, -h    Show this help message"
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                exit 1
                ;;
        esac
    done
    
    if [ "$install_method" = "homebrew" ]; then
        install_via_homebrew
    else
        # Default: install binary directly
        local version="${VERSION:-$(get_latest_version)}"
        if [ -z "$version" ]; then
            log_error "Could not determine latest version"
            exit 1
        fi
        
        log_info "Installing ai-usage ${version}..."
        install_binary "$version"
    fi
    
    # Print next steps
    echo ""
    log_info "Next steps:"
    echo "  1. Copy config: cp config.example.yaml ${CONFIG_DIR}/config.yaml"
    echo "  2. Edit config: vim ${CONFIG_DIR}/config.yaml"
    echo "  3. Run: ${BINARY_NAME}"
    echo "  4. Enable auto-start: ${BINARY_NAME} daemon install"
}

main "$@"
