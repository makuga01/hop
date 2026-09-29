#!/bin/sh
# Install the latest public GitHub release. Never edit PATH or shell profiles.

hop_fail() {
    printf 'hop: %s\n' "$*" >&2
    exit 1
}

hop_download() {
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 \
        -fLsS --retry 2 --connect-timeout 10 --max-time 120 \
        -o "$2" "$1" || hop_fail "Download failed. No installation was changed."
}

hop_cleanup() {
    if [ -n "$hop_staged_binary" ]; then
        rm -f "$hop_staged_binary"
    fi
    rm -rf "$hop_tmp_dir"
}

hop_install() {
    set -eu

    case "$(uname -s)" in
        Darwin) hop_platform=darwin ;;
        Linux) hop_platform=linux ;;
        *) hop_fail 'Supported systems: macOS and Linux.' ;;
    esac
    case "$(uname -m)" in
        arm64|aarch64) hop_arch=arm64 ;;
        x86_64|amd64) hop_arch=amd64 ;;
        *) hop_fail 'Supported processors: ARM64 and AMD64.' ;;
    esac
    for hop_command in curl awk tar mktemp mkdir install chmod mv rm; do
        command -v "$hop_command" >/dev/null 2>&1 || hop_fail "Required command missing: $hop_command"
    done
    if command -v sha256sum >/dev/null 2>&1; then
        hop_checksum_command=sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        hop_checksum_command=shasum
    else
        hop_fail 'SHA-256 verification requires sha256sum or shasum.'
    fi

    hop_install_dir=${HOP_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}
    case "$hop_install_dir" in
        /*) ;;
        *) hop_install_dir="$(pwd)/$hop_install_dir" ;;
    esac
    hop_destination="$hop_install_dir/hop"
    if [ -L "$hop_destination" ] || [ -d "$hop_destination" ]; then
        hop_fail "Refusing to replace a symlink or directory: $hop_destination"
    fi

    hop_tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/hop-install.XXXXXX")
    hop_staged_binary=
    trap hop_cleanup 0
    trap 'exit 1' HUP INT TERM

    hop_releases=https://github.com/makuga01/hop/releases
    hop_download "$hop_releases/latest/download/SHA256SUMS" "$hop_tmp_dir/SHA256SUMS"
    hop_pattern="^hop_[0-9][0-9A-Za-z.-]*_${hop_platform}_${hop_arch}\\.tar\\.gz$"
    hop_archive=$(awk -v pattern="$hop_pattern" 'NF == 2 && $2 ~ pattern {print $2}' "$hop_tmp_dir/SHA256SUMS")
    hop_expected=$(awk -v pattern="$hop_pattern" 'NF == 2 && $2 ~ pattern {print $1}' "$hop_tmp_dir/SHA256SUMS")
    case "$hop_archive" in
        ''|*[!a-zA-Z0-9._-]*) hop_fail 'No unique release archive found for this platform.' ;;
    esac
    [ "${#hop_expected}" -eq 64 ] || hop_fail 'Invalid release checksum.'
    case "$hop_expected" in
        *[!0-9a-f]*) hop_fail 'Invalid release checksum.' ;;
    esac
    hop_version=${hop_archive#hop_}
    hop_version=${hop_version%"_${hop_platform}_${hop_arch}.tar.gz"}

    printf 'Downloading Hop %s for %s/%s…\n' "$hop_version" "$hop_platform" "$hop_arch"
    # Pin the archive to the manifest's version even if "latest" changes now.
    hop_download "$hop_releases/download/v$hop_version/$hop_archive" "$hop_tmp_dir/$hop_archive"
    if [ "$hop_checksum_command" = sha256sum ]; then
        hop_actual=$(sha256sum "$hop_tmp_dir/$hop_archive" | awk '{print $1}')
    else
        hop_actual=$(shasum -a 256 "$hop_tmp_dir/$hop_archive" | awk '{print $1}')
    fi
    [ "$hop_actual" = "$hop_expected" ] || hop_fail 'Checksum mismatch. No installation was changed.'

    # Read only the binary to stdout; never extract archive paths into the filesystem.
    tar -xOf "$hop_tmp_dir/$hop_archive" hop > "$hop_tmp_dir/hop" || hop_fail 'Could not extract Hop.'
    [ -s "$hop_tmp_dir/hop" ] || hop_fail 'The archive contains no Hop binary.'
    chmod 755 "$hop_tmp_dir/hop"
    [ "$("$hop_tmp_dir/hop" --version)" = "hop $hop_version" ] || hop_fail 'Downloaded binary did not pass its version check.'

    mkdir -p "$hop_install_dir"
    hop_staged_binary=$(mktemp "$hop_install_dir/.hop-install.XXXXXX")
    install -m 755 "$hop_tmp_dir/hop" "$hop_staged_binary"
    mv -f "$hop_staged_binary" "$hop_destination"
    hop_staged_binary=
    printf 'Installed Hop %s to %s\n' "$hop_version" "$hop_destination"
    printf 'PATH and shell startup files were not changed.\n'
    if [ "$(command -v hop || true)" = "$hop_destination" ]; then
        printf 'Run: hop\n'
    else
        printf 'Run: "%s"\n' "$hop_destination"
    fi
}

hop_install "$@"
