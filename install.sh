#!/bin/sh
# install.sh — install a prebuilt Enigma release (SPEC §11).
#
#   curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/enigma/main/install.sh | sh
#
# `sh install.sh --help` lists the knobs. Asset names follow .goreleaser.yaml
# and internal/release: enigma_<version>_<os>_<arch>.tar.gz + checksums.txt.
set -eu

REPO="nadavushka-dev/enigma"
BASE_URL="${ENIGMA_RELEASES_URL:-https://github.com/$REPO/releases}"
BASE_URL="${BASE_URL%/}"

say() { printf 'enigma-install: %s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat <<'EOT'
install.sh — install a prebuilt Enigma release

  curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/enigma/main/install.sh | sh

Picks the release asset for this machine (macOS or Linux, amd64 or arm64),
verifies its sha256 against the release's checksums.txt, and installs the
binary to ~/.local/bin (falling back to /usr/local/bin, with sudo if needed).

Environment:
  ENIGMA_VERSION       release to install, e.g. v0.1.0 (default: the latest)
  ENIGMA_INSTALL_DIR   install directory (default: ~/.local/bin)
  ENIGMA_RELEASES_URL  releases base URL (default: https://github.com/nadavushka-dev/enigma/releases;
                       a file:// directory with the same layout also works)

Arguments:
  --dry-run   print what would be installed, install nothing
  --help      this text

Needs curl or wget, tar, and sha256sum, shasum or openssl.
Machines you add with `enigma machine add` get Enigma installed for you.
EOT
}

detect_os() {
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	*) die "unsupported OS $(uname -s): Enigma runs on macOS and Linux" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture $(uname -m): amd64 and arm64 only" ;;
	esac
}

fetch() { # fetch URL DEST
	case "$1" in
	file://*) cp "${1#file://}" "$2" 2>/dev/null ;;
	*)
		if have curl; then
			curl -fsSL --retry 3 -o "$2" "$1"
		elif have wget; then
			wget -q -O "$2" "$1"
		else
			die "need curl or wget"
		fi
		;;
	esac
}

# GitHub redirects <releases>/latest to <releases>/tag/<tag>; following the
# redirect avoids the API and its rate limit. A file:// mirror has a
# "latest" file instead.
latest_tag() {
	case "$BASE_URL" in
	file://*)
		tag=$(cat "${BASE_URL#file://}/latest" 2>/dev/null) || die "no release found in ${BASE_URL#file://}"
		echo "$tag"
		return
		;;
	esac
	url=
	if have curl; then
		url=$(curl -fsSIL --retry 3 -o /dev/null -w '%{url_effective}' "$BASE_URL/latest") || url=
	elif have wget; then
		url=$(wget -q -S --spider "$BASE_URL/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -n 1)
	fi
	case "$url" in
	*/tag/*) echo "${url##*/tag/}" ;;
	*) die "no release found at $BASE_URL/latest (set ENIGMA_VERSION to pin one)" ;;
	esac
}

resolve_tag() {
	case "${ENIGMA_VERSION:-latest}" in
	latest) latest_tag ;;
	v*) echo "$ENIGMA_VERSION" ;;
	*) echo "v$ENIGMA_VERSION" ;;
	esac
}

sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif have openssl; then
		openssl dgst -sha256 "$1" | sed 's/.*= //'
	else
		die "need sha256sum, shasum or openssl to verify the download"
	fi
}

verify_sha256() { # verify_sha256 FILE CHECKSUMS NAME
	want=$(awk -v n="$3" '$2 == n { print $1 }' "$2")
	[ -n "$want" ] || die "$3 is not listed in checksums.txt"
	got=$(sha256_of "$1")
	[ "$got" = "$want" ] || die "sha256 mismatch for $3: want $want, got $got"
}

writable_dir() { mkdir -p "$1" 2>/dev/null && [ -w "$1" ]; }

as_root() {
	if [ -n "$SUDO" ]; then sudo "$@"; else "$@"; fi
}

# install_bin SRC: places SRC at $dir/enigma atomically (tmp + mv), falling
# back from ~/.local/bin to /usr/local/bin, and to sudo. Sets $dir.
install_bin() {
	SUDO=
	if ! writable_dir "$dir"; then
		if [ -z "${ENIGMA_INSTALL_DIR:-}" ]; then
			say "$dir is not writable; falling back to /usr/local/bin"
			dir=/usr/local/bin
		fi
		if ! writable_dir "$dir"; then
			have sudo || die "$dir is not writable and sudo is not installed; set ENIGMA_INSTALL_DIR"
			say "$dir is not writable; using sudo"
			SUDO=sudo
		fi
	fi
	as_root mkdir -p "$dir"
	as_root cp "$1" "$dir/.enigma.tmp"
	as_root chmod 0755 "$dir/.enigma.tmp"
	as_root mv -f "$dir/.enigma.tmp" "$dir/enigma"
}

path_hint() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	esac
	say "note: $1 is not on your PATH; add it to your shell rc:"
	say "  export PATH=\"$1:\$PATH\""
}

main() {
	dry_run=0
	for arg in "$@"; do
		case "$arg" in
		--dry-run) dry_run=1 ;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown argument $arg (try --help)" ;;
		esac
	done

	os=$(detect_os) || exit 1
	arch=$(detect_arch) || exit 1
	tag=$(resolve_tag) || exit 1
	asset="enigma_${tag#v}_${os}_${arch}.tar.gz"
	url="$BASE_URL/download/$tag/$asset"
	dir="${ENIGMA_INSTALL_DIR:-$HOME/.local/bin}"

	if [ "$dry_run" = 1 ]; then
		printf 'os=%s\narch=%s\nversion=%s\nurl=%s\ninstall_dir=%s\n' "$os" "$arch" "$tag" "$url" "$dir"
		exit 0
	fi

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t enigma)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "downloading $url"
	fetch "$url" "$tmp/$asset" || die "download failed: $url"
	fetch "$BASE_URL/download/$tag/checksums.txt" "$tmp/checksums.txt" || die "download failed: checksums.txt"
	verify_sha256 "$tmp/$asset" "$tmp/checksums.txt" "$asset"
	tar -xzf "$tmp/$asset" -C "$tmp" enigma

	install_bin "$tmp/enigma"
	ver=$("$dir/enigma" version) || die "$dir/enigma does not run on this machine"
	say "installed $ver to $dir/enigma"
	path_hint "$dir"
	say "next: \`enigma init\` on your first machine, then \`enigma service install\`;"
	say "      other machines are added from it with \`enigma machine add <ssh-alias>\`"
}

main "$@"
