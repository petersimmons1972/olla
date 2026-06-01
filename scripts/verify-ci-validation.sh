#!/usr/bin/env bash
set -euo pipefail

grep -Eq 'go test .*coverprofile=coverage\.out \./\.\.\.' makefile
grep -q 'make test-cover' .github/workflows/ci.yml
grep -q 'file: ./coverage.out' .github/workflows/ci.yml
grep -q 'make test-reliability' .github/workflows/ci.yml
grep -q '^test-reliability:' makefile

grep -q 'go mod verify' .github/workflows/release.yml
grep -q 'go vet ./...' .github/workflows/release.yml
grep -q 'make test' .github/workflows/release.yml
grep -q 'make build' .github/workflows/release.yml

if grep -q 't.Skip("JSONPath validation' internal/adapter/metrics/extractor_test.go; then
	echo "metrics JSONPath validation test is still skipped" >&2
	exit 1
fi
