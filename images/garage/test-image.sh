#!/bin/sh
set -eu

image="${1:-cf-esb-garage:port-test}"
docker build --tag "$image" "$(dirname "$0")"

exposed_ports="$(docker image inspect --format '{{json .Config.ExposedPorts}}' "$image")"
if [ "$exposed_ports" != '{"3900/tcp":{}}' ]; then
	printf 'unexpected image exposed ports: %s\n' "$exposed_ports" >&2
	exit 1
fi

printf 'Garage image exposes TCP/3900 (%s)\n' "$image"
