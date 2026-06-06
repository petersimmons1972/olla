#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_mount="./olla.yaml:/app/config/docker.yaml:ro"
failed=0

while IFS= read -r compose_file; do
	if ! grep -q "ghcr.io/thushan/olla" "$compose_file"; then
		continue
	fi

	if grep -q "/app/config.yaml" "$compose_file"; then
		echo "$compose_file: Olla config mounts must not target ignored /app/config.yaml"
		failed=1
	fi

	if ! grep -qF -- "$expected_mount" "$compose_file"; then
		echo "$compose_file: missing expected Olla config mount $expected_mount"
		failed=1
	fi
done < <(find "$root/examples" -name "compose.yaml" -print | sort)

if grep -R --line-number "/app/config.yaml" "$root/examples" >/tmp/olla-example-config-docs.$$; then
	cat /tmp/olla-example-config-docs.$$
	rm -f /tmp/olla-example-config-docs.$$
	echo "examples docs must reference /app/config/docker.yaml for container config overrides"
	failed=1
else
	rm -f /tmp/olla-example-config-docs.$$
fi

exit "$failed"
