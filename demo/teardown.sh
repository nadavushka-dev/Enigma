#!/bin/sh
# Removes what setup.sh staged.
DEMO=${DEMO:-/tmp/enigma-demo}
[ -f "$DEMO/env" ] && (. "$DEMO/env"; enigma service uninstall) >/dev/null 2>&1
docker rm -f enigma-demo-devbox enigma-demo-nas >/dev/null 2>&1
docker network rm enigma-demo-net >/dev/null 2>&1
rm -rf "$DEMO"
