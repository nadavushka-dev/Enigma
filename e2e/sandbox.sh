#!/bin/sh
# Sandbox: three throwaway Linux machines (s1 s2 s3) running the enigma
# built from this working tree, linked over real ssh, for trying things by
# hand. Nothing on this Mac is touched.
#
#   e2e/sandbox.sh up          build, start the boxes, form the fleet
#   e2e/sandbox.sh shell s2    a shell on s2 (then: enigma tui, enigma group ...)
#   e2e/sandbox.sh run s1 enigma status
#   e2e/sandbox.sh down        remove the boxes
set -eu
cd "$(dirname "$0")/.."
image=enigma-e2e:latest
net=enigma-sandbox-net
dir=${ENIGMA_SANDBOX_DIR:-/tmp/enigma-sandbox}
boxes="s1 s2 s3"

cname() { echo "enigma-sandbox-$1"; }
sh_in() { box=$1; shift; docker exec -it -u dev -e HOME=/home/dev -e TERM="${TERM:-xterm-256color}" "$(cname "$box")" "$@"; }

up() {
	docker image inspect "$image" >/dev/null 2>&1 || docker build -q -t "$image" e2e/docker
	mkdir -p "$dir/keys" "$dir/bin"
	if [ ! -f "$dir/keys/id_ed25519" ]; then
		ssh-keygen -q -t ed25519 -N "" -f "$dir/keys/id_ed25519"
		cp "$dir/keys/id_ed25519.pub" "$dir/keys/authorized_keys"
	fi
	version=${VERSION:-v0.0.0-dev.$(TZ=UTC git show -s --format=%cd --date=format-local:%Y%m%d%H%M%S HEAD).$(git rev-parse --short=7 HEAD)}
	arch=$(docker info --format '{{.Architecture}}' | sed 's/x86_64/amd64/; s/aarch64/arm64/')
	echo "building $version for linux/$arch"
	CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -ldflags "-X github.com/nadavushka-dev/enigma/internal/buildinfo.Version=$version" -o "$dir/bin/enigma" ./cmd/enigma
	docker network inspect "$net" >/dev/null 2>&1 || docker network create "$net" >/dev/null
	for b in $boxes; do
		docker rm -f "$(cname "$b")" >/dev/null 2>&1 || true
		docker run -d --name "$(cname "$b")" --hostname "$b" --network "$net" --network-alias "$b" \
			-v "$dir/bin/enigma:/usr/local/bin/enigma:ro" -v "$dir/keys:/keys:ro" "$image" >/dev/null
		docker exec "$(cname "$b")" sh -c 'cp /keys/id_ed25519 /home/dev/.ssh/id && chown dev:dev /home/dev/.ssh/id && chmod 600 /home/dev/.ssh/id'
	done
	run s1 enigma init --name s1
	run s1 enigma service install
	for b in s2 s3; do run s1 enigma machine add "$b"; done
	echo
	echo "sandbox is up. try:"
	echo "  e2e/sandbox.sh shell s1      # then: enigma tui"
	echo "  e2e/sandbox.sh run s2 enigma status"
}

run() { box=$1; shift; docker exec -u dev -e HOME=/home/dev "$(cname "$box")" "$@"; }

down() {
	for b in $boxes; do docker rm -f "$(cname "$b")" >/dev/null 2>&1 || true; done
	docker network rm "$net" >/dev/null 2>&1 || true
	echo "sandbox removed"
}

case "${1:-}" in
up) up ;;
down) down ;;
shell) sh_in "${2:?box}" sh -l ;;
run) shift; run "$@" ;;
*) sed -n '2,10p' "$0"; exit 2 ;;
esac
