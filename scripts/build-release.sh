#!/usr/bin/env sh
# Build self-contained release binaries for the common desktop/server targets.
# Requires Go, Node.js and npm. Docker is not involved.
set -eu

version="${VERSION:-0.1.1}"
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

cd "$root/web"
npm ci
npm run build
cd "$root"

release="$root/dist"
rm -rf "$release"
mkdir -p "$release"

for target in ${TARGETS:-windows/amd64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}; do
  goos="${target%/*}"
  goarch="${target#*/}"
  name="metricspanel_${version}_${goos}_${goarch}"
  dir="$release/$name"
  mkdir -p "$dir"
  file="metricspanel"
  if [ "$goos" = "windows" ]; then file="metricspanel.exe"; fi

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -tags webui -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$dir/$file" ./cmd/metricspanel

  cp README.md LICENSE "$dir/"
  (cd "$dir" && tar -czf "../$name.tar.gz" .)
  echo "built $name"
done

echo "Release artifacts are in $release"
