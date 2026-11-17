#!/usr/bin/env bash
# Run under the shared edge release lock. JSON credentials arrive only on stdin.
set -Eeuo pipefail
[[ $(id -un) == deploy && $(readlink -f /opt/maxim-dev) == /opt/maxim-dev ]]
root=/opt/maxim-dev
python3 "$root/configure-telegram.py" dev
active=$(sed -n 's/^ACTIVE_SLOT=//p' "$root/slots.env" | tr -d '\r')
[[ $active == a || $active == b ]]
tag=$(sed -n "s/^MAXIM_TAG_${active^^}=//p" "$root/slots.env" | tr -d '\r')
[[ $tag =~ ^sha-[a-f0-9]{40}$ ]]
"$root/release.sh" --tag "$tag" --reapply --skip-pull --dir "$root" \
  --expected-domain dev.chanly.ru --edge-dir /opt/edge
python3 "$root/configure-telegram.py" dev --verify
