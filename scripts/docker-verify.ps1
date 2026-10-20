param(
  [int]$Port = 17333,
  [string]$Token = "",
  # Production uses Alpine; BusyBox is a local offline fallback for smoke runs
  # when Docker Hub is unavailable.
  [string]$BaseImage = "busybox:1.37.0"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$name = "metricspanel-verify-$PID"
$image = "metricspanel-verify:$PID"
$volume = "metricspanel-verify-$PID"
$work = Join-Path $env:TEMP "metricspanel-docker-$PID"

if (-not $Token) {
  $Token = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 32 | ForEach-Object { [char]$_ })
}

Write-Host "Docker verification before:"
docker system df

try {
  if (-not (Test-Path (Join-Path $root "web/dist/index.html"))) {
    throw "Frontend bundle missing. Run 'npm run build' in web first."
  }
  New-Item -ItemType Directory -Force $work | Out-Null

  $env:CGO_ENABLED = "0"
  $env:GOOS = "linux"
  $env:GOARCH = "amd64"
  Push-Location $root
  try {
    go build -tags webui -trimpath -ldflags "-s -w" -o (Join-Path $work "metricspanel") ./cmd/metricspanel
    if ($LASTEXITCODE -ne 0) { throw "Linux cross-build failed" }
  } finally {
    Pop-Location
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
  }

  @"
FROM $BaseImage
RUN addgroup -g 10001 metricspanel && adduser -D -H -u 10001 -G metricspanel metricspanel && mkdir /data && chown metricspanel:metricspanel /data
COPY metricspanel /usr/local/bin/metricspanel
USER metricspanel
EXPOSE 7333
VOLUME /data
ENTRYPOINT ["metricspanel"]
CMD ["serve","--listen","0.0.0.0:7333","--db","/data/control.db","--auth","local","--identity-db","/data/identity.db"]
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s CMD metricspanel health || exit 1
"@ | Set-Content -Encoding ASCII (Join-Path $work "Dockerfile")

  Write-Host "Building the minimal Linux runtime image..."
  docker build --quiet --label metricspanel.verify=true -t $image $work | Out-Host
  if ($LASTEXITCODE -ne 0) { throw "docker build failed" }
  $size = [int64](docker image inspect $image --format "{{.Size}}")
  $limit = 1GB
  if ($size -gt $limit) { throw "Image is $([math]::Round($size/1MB,1)) MiB, above the 1 GiB budget" }
  Write-Host ("Image size: {0:N1} MiB" -f ($size / 1MB))

  docker volume create --label metricspanel.verify=true $volume | Out-Null
  docker run -d --name $name --label metricspanel.verify=true `
    -p "127.0.0.1:${Port}:7333" `
    -e "METRICSPANEL_TOKEN=$Token" `
    -e "METRICSPANEL_AUTH=local" `
    -e "METRICSPANEL_STORAGE=sqlite" `
    -v "${volume}:/data" $image | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "docker run failed" }

  $setup = ""
  $logs = ""
  $deadline = (Get-Date).AddSeconds(30)
  do {
    Start-Sleep -Seconds 1
    $logs = (docker logs $name 2>&1 | Out-String)
    $match = [regex]::Match($logs, "Setup token: (\S+)")
    if ($match.Success) { $setup = $match.Groups[1].Value }
  } while (-not $setup -and (Get-Date) -lt $deadline)
  if (-not $setup) { throw "Container did not print a setup token:`n$logs" }

  Push-Location (Join-Path $root "web")
  try {
    $env:METRICSPANEL_EXTERNAL_URL = "http://127.0.0.1:$Port"
    $env:METRICSPANEL_SETUP_TOKEN = $setup
    node scripts/auth-smoke.mjs
    if ($LASTEXITCODE -ne 0) { throw "Browser verification against the Linux container failed" }
  } finally {
    Pop-Location
    Remove-Item Env:METRICSPANEL_EXTERNAL_URL, Env:METRICSPANEL_SETUP_TOKEN -ErrorAction SilentlyContinue
  }

  Write-Host "Checking the container health command..."
  docker exec $name metricspanel health | Out-Host
  if ($LASTEXITCODE -ne 0) { throw "metricspanel health failed inside the container" }
  Write-Host "Linux container verification passed."
} finally {
  docker rm -f $name 2>$null | Out-Null
  docker volume rm $volume 2>$null | Out-Null
  docker image rm $image 2>$null | Out-Null
  # The tiny image still creates BuildKit layers. Remove unused build cache so
  # repeated verification does not grow the Docker data directory.
  docker builder prune -f 2>$null | Out-Null
  docker image prune -f 2>$null | Out-Null
  Remove-Item $work -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item (Join-Path $root "web/test-results"), (Join-Path $root "web/playwright-report") -Recurse -Force -ErrorAction SilentlyContinue
  Write-Host "Docker verification after cleanup:"
  docker system df
  Write-Host "Only artifacts created by this run were removed."
}
