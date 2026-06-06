#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failed=0

check_absent() {
	local pattern="$1"
	local message="$2"
	if grep -R --exclude='reference.md' --line-number -E "$pattern" "$root/docs/content" "$root/examples"; then
		echo "$message"
		failed=1
	fi
}

check_absent 'OLLA_SERVER_REQUEST_LOGGING' \
	"OLLA_SERVER_REQUEST_LOGGING is not a supported documented env override."

check_absent 'OLLA_SERVER_RATE_LIMITS(_|\*)' \
	"OLLA_SERVER_RATE_LIMITS_* is not a supported documented env override; use the flat rate-limit env vars."

check_absent 'All configuration values can be overridden|All configuration can be overridden|all config values can be overridden' \
	"Docs must not claim every config value has an env override."

if grep -R --line-number -E 'OLLA_LOG_LEVEL.*(deprecated|unsupported)|(deprecated|unsupported).*OLLA_LOG_LEVEL' "$root/docs/content" "$root/examples"; then
	echo "OLLA_LOG_LEVEL is intentionally supported by logger bootstrap and must not be documented as deprecated/unsupported."
	failed=1
fi

exit "$failed"
