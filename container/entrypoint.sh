#!/bin/sh
# Starts display-controller with a writable config.yaml.
#
# The Web GUI saves its changes back to the config file, so it lives on the
# data volume. On the first start it is copied from the seed: the image's
# default, or a ConfigMap mounted over /etc/booth-display. Delete
# /data/config.yaml to start over from the seed.
#
# Arguments are passed on to display-controller (e.g. -auto-start).
set -eu

CONFIG="${BOOTH_CONFIG:-/data/config.yaml}"
SEED="${BOOTH_CONFIG_SEED:-/etc/booth-display/config.yaml}"

if [ ! -e "$CONFIG" ]; then
    echo "booth-display: $CONFIG not found; copying $SEED"
    cp "$SEED" "$CONFIG"
fi

exec display-controller -config "$CONFIG" "$@"
