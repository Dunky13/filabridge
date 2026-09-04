param(
    [Parameter(Mandatory = $true)]
    [string]$Artifact
)

$ErrorActionPreference = "Stop"
$artifactPath = (Resolve-Path $Artifact).Path
$smokeDirectory = Join-Path $env:RUNNER_TEMP ("filabridge-smoke-" + [guid]::NewGuid())
$dataDirectory = Join-Path $smokeDirectory "data"
$stdoutLog = Join-Path $smokeDirectory "stdout.log"
$stderrLog = Join-Path $smokeDirectory "stderr.log"
$port = if ($env:FILABRIDGE_SMOKE_PORT) { $env:FILABRIDGE_SMOKE_PORT } else { "59321" }

New-Item -ItemType Directory -Path $dataDirectory | Out-Null
$env:FILABRIDGE_DB_PATH = $dataDirectory
$process = $null

try {
    $process = Start-Process -FilePath $artifactPath -ArgumentList @(
        "--web-only", "--host", "127.0.0.1", "--port", $port
    ) -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog -PassThru

    $healthy = $false
    foreach ($attempt in 1..60) {
        if ($process.HasExited) {
            throw "Artifact exited before becoming healthy.`n$(Get-Content $stdoutLog -Raw)`n$(Get-Content $stderrLog -Raw)"
        }
        try {
            Invoke-WebRequest -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 2 | Out-Null
            $healthy = $true
            break
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }

    if (-not $healthy) {
        throw "Artifact did not become healthy.`n$(Get-Content $stdoutLog -Raw)`n$(Get-Content $stderrLog -Raw)"
    }

    $database = Join-Path $dataDirectory "filabridge.db"
    if (-not (Test-Path $database) -or (Get-Item $database).Length -eq 0) {
        throw "Artifact served HTTP but did not initialize SQLite database: $database"
    }

    Write-Output "Artifact smoke test passed: $([IO.Path]::GetFileName($artifactPath))"
} finally {
    if ($process -and -not $process.HasExited) {
        try {
            Stop-Process -Id $process.Id -Force
            $process.WaitForExit()
        } catch {
            # The process may exit between the HasExited check and Stop-Process.
        }
    }
    Remove-Item -Path $smokeDirectory -Recurse -Force -ErrorAction SilentlyContinue
}
