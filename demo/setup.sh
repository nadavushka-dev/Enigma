#!/bin/sh
# Stages the README demo: a throwaway "mac" (this machine, under $DEMO, with
# a background daemon) and two Linux containers "devbox" and "nas" with
# sshd, on one Docker network so they reach each other. Nothing
# touches the real ~, ~/.ssh or an installed Enigma. Undo with teardown.sh.
set -eu
cd "$(dirname "$0")/.."
DEMO=${DEMO:-/tmp/enigma-demo}
NAME=enigma-demo-devbox
ARCH=$(go env GOARCH)
VERSION=${VERSION:-v0.3.0} # what the recording shows

sh demo/teardown.sh >/dev/null 2>&1 || true
mkdir -p "$DEMO/bin" "$DEMO/home/.ssh" "$DEMO/keys"

make build VERSION="$VERSION" >/dev/null
cp bin/enigma "$DEMO/bin/enigma"
make dist VERSION="$VERSION" DIST="$DEMO/enigma/bin/$VERSION" PLATFORMS="linux/$ARCH" >/dev/null

ssh-keygen -q -t ed25519 -N '' -f "$DEMO/keys/id_ed25519"
cp "$DEMO/keys/id_ed25519.pub" "$DEMO/keys/authorized_keys"
docker image inspect enigma-e2e:latest >/dev/null 2>&1 || docker build -q -t enigma-e2e:latest e2e/docker >/dev/null
docker network inspect enigma-demo-net >/dev/null 2>&1 || docker network create enigma-demo-net >/dev/null
for h in devbox nas; do
	docker run -d --name "enigma-demo-$h" --hostname "$h" --network enigma-demo-net --network-alias "$h" \
		-p 127.0.0.1::22 -v "$DEMO/keys:/keys:ro" enigma-e2e:latest >/dev/null
	docker exec "enigma-demo-$h" sh -c 'cp /keys/id_ed25519 /home/dev/.ssh/id && chown dev:dev /home/dev/.ssh/id && chmod 600 /home/dev/.ssh/id'
done
PORT=$(docker port "$NAME" 22/tcp | sed 's/.*://')
NASPORT=$(docker port enigma-demo-nas 22/tcp | sed 's/.*://')

# The demo's ssh: the system one with its own config.
cat > "$DEMO/home/.ssh/config" <<CFG
Host devbox
  HostName 127.0.0.1
  Port $PORT
  User dev
  IdentityFile $DEMO/keys/id_ed25519
  IdentitiesOnly yes
  IdentityAgent none
  UserKnownHostsFile /dev/null
  StrictHostKeyChecking no
  LogLevel ERROR

Host nas
  HostName 127.0.0.1
  Port $NASPORT
  User dev
  IdentityFile $DEMO/keys/id_ed25519
  IdentitiesOnly yes
  IdentityAgent none
  UserKnownHostsFile /dev/null
  StrictHostKeyChecking no
  LogLevel ERROR

Host laptop
  HostName laptop.invalid
CFG
printf '#!/bin/sh\nexec /usr/bin/ssh -F %s "$@"\n' "$DEMO/home/.ssh/config" > "$DEMO/bin/ssh"
chmod +x "$DEMO/bin/ssh"

mkdir -p "$DEMO/enigma" && echo 'notifications = false' > "$DEMO/enigma/config.toml"

cat > "$DEMO/env" <<ENV
export HOME=$DEMO/home ENIGMA_HOME=$DEMO/enigma ENIGMA_SERVICE=background
export PATH=$DEMO/bin:\$PATH PS1='mac ~ \$ '
cd ~
ENV
# wait for sshd
for h in devbox nas; do
	for i in 1 2 3 4 5 6 7 8 9 10; do "$DEMO/bin/ssh" "$h" true 2>/dev/null && break; sleep 1; done
done
echo "demo staged in $DEMO (source $DEMO/env)"
