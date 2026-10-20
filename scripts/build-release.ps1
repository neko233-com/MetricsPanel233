param(
  [string]$Version = "0.1.1",
  [string[]]$Targets = @("windows/amd64", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

Push-Location (Join-Path $root "web")
try {
  npm ci
  if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
  npm run build
  if ($LASTEXITCODE -ne 0) { throw "frontend build failed" }
} finally {
  Pop-Location
}

$release = Join-Path $root "dist"
if (Test-Path $release) { Remove-Item $release -Recurse -Force }
New-Item -ItemType Directory -Path $release | Out-Null

$env:CGO_ENABLED = "0"
foreach ($target in $Targets) {
  $parts = $target.Split("/")
  $goos, $goarch = $parts[0], $parts[1]
  $name = "metricspanel_${Version}_${goos}_${goarch}"
  $dir = Join-Path $release $name
  New-Item -ItemType Directory -Path $dir | Out-Null
  $file = "metricspanel"
  if ($goos -eq "windows") { $file = "metricspanel.exe" }

  $env:GOOS = $goos
  $env:GOARCH = $goarch
  go build -tags webui -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $dir $file) ./cmd/metricspanel
  if ($LASTEXITCODE -ne 0) { throw "build failed for $target" }

  Copy-Item (Join-Path $root "README.md"), (Join-Path $root "LICENSE") -Destination $dir
  Compress-Archive -Path (Join-Path $dir "*") -DestinationPath (Join-Path $release "$name.zip") -Force
  Write-Host "built $name"
}

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
Write-Host "Release artifacts are in $release"
