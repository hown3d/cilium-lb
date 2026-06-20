#!/usr/bin/env bash
set -eo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && /bin/pwd)"

node=$1
shift

docker build -t ttl.sh/xdptools --platform=linux/amd64 --platform=linux/arm64 --push $DIR

cat <<EOF >/tmp/profile.yaml
securityContext:
  runAsNonRoot: false
  runAsUser: 0
  runAsGroup: 0
  privileged: true
EOF

kubectl debug node/${node} -ti --custom=/tmp/profile.yaml --profile=netadmin --image=ttl.sh/xdptools -- "$@"
