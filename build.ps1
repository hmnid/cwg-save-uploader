$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    New-Item -ItemType Directory -Force -Path dist | Out-Null
    go build -trimpath '-ldflags=-H=windowsgui -s -w' -o dist/cwg2-uploader.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Tray build failed' }
    go build -trimpath '-ldflags=-s -w' -o dist/cwg2-uploader-console.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Console build failed' }
    & (Join-Path $PSScriptRoot 'scripts\notices.ps1')
    Copy-Item README.md, LICENSE, THIRD_PARTY_NOTICES.md -Destination dist
    Write-Host 'Built dist/cwg2-uploader.exe and dist/cwg2-uploader-console.exe'
} finally {
    Pop-Location
}
