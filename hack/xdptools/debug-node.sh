#!/usr/bin/env bash
set -eo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && /bin/pwd)"
PLATFORM=amd64

node=$1
shift

docker build -t ttl.sh/xdptools --platform=linux/${PLATFORM} --push $DIR

cat <<EOF >/tmp/profile.yaml
securityContext:
  runAsNonRoot: false
  runAsUser: 0
  runAsGroup: 0
  privileged: true
EOF

kubectl debug node/${node} -ti --custom=/tmp/profile.yaml --profile=netadmin --image=ttl.sh/xdptools -- "$@"
