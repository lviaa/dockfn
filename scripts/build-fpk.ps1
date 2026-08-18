[CmdletBinding()]
param(
    [string]$Version
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
Set-Location -LiteralPath $repoRoot

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $false)][string[]]$Arguments = @()
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE"
    }
}

function Require-Command {
    param([Parameter(Mandatory = $true)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command not found: $Name"
    }
}

if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = (Get-Content -LiteralPath (Join-Path $repoRoot 'VERSION') -Encoding UTF8 -TotalCount 1).Trim()
}
if ($Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z]+)*$') {
    throw "VERSION must contain a release version such as 0.1.1; got '$Version'"
}

foreach ($command in @('go', 'node', 'npm', 'bash')) {
    Require-Command $command
}

$goVersion = (& go version)
if ($goVersion -notmatch 'go1\.26(?:\.|$)') {
    throw "Go 1.26 is required; detected $goVersion"
}
$nodeVersion = (& node --version)
if ($nodeVersion -notmatch '^v24\.') {
    throw "Node.js 24 is required; detected $nodeVersion"
}

$buildTime = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$commit = 'manual'
try {
    $candidateCommit = (& git rev-parse --short HEAD 2>$null).Trim()
    if ($candidateCommit) { $commit = $candidateCommit }
} catch {
    # Git metadata is optional for a local package build.
}
$ldflags = "-s -w -X main.version=$Version -X main.commit=$commit -X main.buildTime=$buildTime"

Write-Host "DockFN Windows FPK build: version $Version"
Write-Host 'Installing frontend dependencies...'
Invoke-Checked 'npm' @('--prefix', 'web', 'ci', '--ignore-scripts')

Write-Host 'Generating brand assets and web icons...'
Invoke-Checked 'go' @('run', './scripts/generate-brand-assets')
Invoke-Checked 'go' @('run', './scripts/generate-web-icons')
Invoke-Checked 'npm' @('--prefix', 'web', 'run', 'build')

$oldCgo = $env:CGO_ENABLED
$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    Write-Host 'Building Windows helper binary...'
    Invoke-Checked 'go' @('build', '-trimpath', '-ldflags', $ldflags, '-o', (Join-Path $repoRoot 'bin/dockfn.exe'), './cmd/dockfn')

    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    Write-Host 'Building Linux x86_64 binary...'
    Invoke-Checked 'go' @('build', '-trimpath', '-ldflags', $ldflags, '-o', (Join-Path $repoRoot 'bin/dockfn-linux-amd64'), './cmd/dockfn')

    $env:GOARCH = 'arm64'
    Write-Host 'Building Linux arm64 binary...'
    Invoke-Checked 'go' @('build', '-trimpath', '-ldflags', $ldflags, '-o', (Join-Path $repoRoot 'bin/dockfn-linux-arm64'), './cmd/dockfn')
} finally {
    $env:CGO_ENABLED = $oldCgo
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
}

Write-Host 'Building FPK archives...'
$packageOutput = Join-Path $repoRoot 'dist/fpk'
$defaultOutputUsable = $true
if (Test-Path -LiteralPath $packageOutput) {
    try {
        $probe = Join-Path $packageOutput ('.dockfn-write-probe-' + $PID)
        New-Item -ItemType Directory -Path $probe -Force | Out-Null
        Remove-Item -LiteralPath $probe -Force -Recurse
    } catch {
        $defaultOutputUsable = $false
    }
}
if (-not $defaultOutputUsable) {
    throw 'The canonical dist/fpk directory is unavailable; close programs using its files and retry'
}
$packageParent = Split-Path -Parent $packageOutput
New-Item -ItemType Directory -Path $packageParent -Force | Out-Null
$packageOutputRelative = $packageOutput.Substring($repoRoot.Length).TrimStart('\', '/') -replace '\\', '/'
$buildRootRelative = 'dist'
$bashBuildCommand = "DOCKFN_FPK_OUT='$packageOutputRelative' DOCKFN_BUILD_ROOT='$buildRootRelative' bash scripts/build-fpk.sh '$Version'"
Invoke-Checked 'bash' @('-c', $bashBuildCommand)

$checksumLines = foreach ($package in (Get-ChildItem -LiteralPath $packageOutput -Filter '*.fpk' | Sort-Object Name)) {
    $hash = (Get-FileHash -LiteralPath $package.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    $relativePackageOutput = $packageOutput.Substring($repoRoot.Length).TrimStart('\', '/') -replace '\\', '/'
    "$hash  $relativePackageOutput/$($package.Name)"
}
if (-not $checksumLines) {
    throw 'No FPK archives were generated'
}
$checksumPath = if ($packageOutput -like (Join-Path $repoRoot 'dist/fpk*')) {
    Join-Path $repoRoot 'dist/SHA256SUMS'
} else {
    Join-Path $packageOutput 'SHA256SUMS'
}
$checksumLines | Set-Content -LiteralPath $checksumPath -Encoding ASCII

Write-Host 'Verifying FPK archives...'
Invoke-Checked 'go' @('run', './scripts/verify-artifacts', '-version', $Version, '-artifact-dir', $packageOutput, '-checksum-file', $checksumPath)

Write-Host ''
Write-Host 'FPK build completed:' -ForegroundColor Green
Get-ChildItem -LiteralPath $packageOutput -Filter '*.fpk' | Sort-Object Name | ForEach-Object {
    Write-Host (" - " + $_.FullName + " (" + $_.Length + ' bytes)')
}
Write-Host (" - " + $checksumPath)
