#!/usr/bin/env bash
# Run KTRX AWS target-health tests with local fixture data only.
# No AWS account, credentials, Kubernetes cluster, or network access is required.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "==> Running offline AWS target-health mock scenario"
env -u AWS_PROFILE -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
  go test ./internal/cli -run '^TestAWSMockTargetHealthScenario$' -count=1 -v

echo
echo "==> Running all Go tests"
go test ./...

echo
echo "==> Running go vet"
go vet ./...

echo
echo "AWS mock scenario, full tests, and go vet passed."
