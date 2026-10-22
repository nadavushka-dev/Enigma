#!/bin/sh
# Stages the README demo: a throwaway "mac" (this machine, under $DEMO, with
# a background daemon) and a Linux container "devbox" with sshd. Nothing
# touches the real ~, ~/.ssh or an installed Enigma. Undo with teardown.sh.
set -eu
cd "$(dirname "$0")/.."
DEMO=${DEMO:-/tmp/enigma-demo}
NAME=enigma-demo-devbox
ARCH=$(go env GOARCH)
VERSION=${VERSION:-v0.1.0} # what the recording shows

sh demo/teardown.sh >/dev/null 2>&1 || true
mkdir -p "$DEMO/bin" "$DEMO/home/.ssh" "$DEMO/keys"

make build VERSION="$VERSION" >/dev/null
cp bin/enigma "$DEMO/bin/enigma"
make dist VERSION="$VERSION" DIST="$DEMO/enigma/bin/$VERSION" PLATFORMS="linux/$ARCH" >/dev/null

ssh-keygen -q -t ed25519 -N '' -f "$DEMO/keys/id_ed25519"
cp "$DEMO/keys/id_ed25519.pub" "$DEMO/keys/authorized_keys"
docker image inspect enigma-e2e:latest >/dev/null 2>&1 || docker build -q -t enigma-e2e:latest e2e/docker >/dev/null
docker run -d --name "$NAME" --hostname devbox -p 127.0.0.1::22 -v "$DEMO/keys:/keys:ro" enigma-e2e:latest >/dev/null
PORT=$(docker port "$NAME" 22/tcp | sed 's/.*://')

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
for i in 1 2 3 4 5 6 7 8 9 10; do "$DEMO/bin/ssh" devbox true 2>/dev/null && break; sleep 1; done
echo "demo staged in $DEMO (source $DEMO/env)"
