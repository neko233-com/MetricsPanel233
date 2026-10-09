param([int]$Repeat = 2)
$ErrorActionPreference = 'Stop'
if ($Repeat -lt 1) { throw 'Repeat must be at least 1' }
$rootPath = Split-Path -Parent $PSScriptRoot
Push-Location $rootPath
try {
    for ($run = 1; $run -le $Repeat; $run++) {
        Write-Host "Docker integration run $run / $Repeat"
        go test -tags=integration ./tests/integration -count=1 -v -timeout=20m
        if ($LASTEXITCODE -ne 0) { throw "Docker integration run $run failed" }
    }
} finally { Pop-Location }
