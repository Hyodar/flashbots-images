#!/bin/bash
set -euxo pipefail

ENV_FILE="env.json"
if [ ! -f "$ENV_FILE" ]; then
    echo "Error: env.json not found"
    exit 1
fi

# Find and process all mustache templates in mkosi.extra directory
find surge-tdx-prover/mkosi.extra -type f -name "*.mustache" | while read -r template; do
    rel_path="${template#surge-tdx-prover/mkosi.extra/}"
    output_path="$BUILDROOT/${rel_path%.mustache}"
    mustache "$ENV_FILE" "$template" > "$output_path"
    rm "$BUILDROOT/$rel_path"
done

# Set permissions of templated files
chmod 644 "$BUILDROOT/etc/nethermind-surge/.env.staging"
chmod 644 "$BUILDROOT/etc/nethermind-surge/.env.hoodi"
chmod 644 "$BUILDROOT/etc/nethermind-surge/raiko_chain_spec_list.json"
