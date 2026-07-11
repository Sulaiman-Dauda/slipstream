#!/usr/bin/env bash
# Build a complete, checksummed Slipstream release with the security-qualified
# toolchain. Usage: scripts/build-release.sh 1.0.0
set -euo pipefail

VERSION="${1:?usage: scripts/build-release.sh <version>}"
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${ROOT}/dist/${VERSION}"
export GOTOOLCHAIN=go1.25.12
export CGO_ENABLED=0

cd "$ROOT"
go test ./...
go vet ./...
if command -v govulncheck >/dev/null; then
  govulncheck ./...
fi
(cd ui && npm ci && npm audit --omit=dev --audit-level=high && npm run build)

rm -rf "$OUT"
mkdir -p "$OUT"
for spec in panel-api:./cmd/panel-api panel-agent:./cmd/panel-agent slipctl:./cmd/slipctl; do
  name=${spec%%:*}
  pkg=${spec#*:}
  GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-s -w -X github.com/slipstream-panel/slipstream/internal/version.Version=${VERSION}" \
    -o "$OUT/$name" "$pkg"
  (cd "$OUT" && sha256sum "$name" > "$name.sha256")
done

cp installer/install.sh "$OUT/"
cp installer/systemd/slipstream-api.service "$OUT/"
cp installer/systemd/slipstream-api.socket "$OUT/"
cp installer/systemd/slipstream-agent.service "$OUT/"
chmod 0755 "$OUT/install.sh"
printf 'Slipstream %s release written to %s\n' "$VERSION" "$OUT"
