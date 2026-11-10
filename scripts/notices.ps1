$ErrorActionPreference = 'Stop'
# Generate redistribution notices from the modules actually linked on Windows.
$lines = go list -deps -f '{{if .Module}}{{.Module.Path}}|{{.Module.Dir}}{{end}}' .
if ($LASTEXITCODE -ne 0) { throw 'Could not enumerate dependencies' }
$root = go env GOROOT
if ($LASTEXITCODE -ne 0) { throw 'Could not locate Go license' }
$sections = [System.Collections.Generic.List[string]]::new()
$sections.Add("# Third-party notices`n`nGenerated from the Windows build dependencies. Includes the Go runtime license.")
$sections.Add("## Go runtime and standard library`n`n" + '```text' + "`n" + [IO.File]::ReadAllText((Join-Path $root 'LICENSE')) + "`n" + '```')
foreach ($line in ($lines | Where-Object { $_ -and ($_ -notlike 'cwg2uploader|*') } | Sort-Object -Unique)) {
    $parts = $line -split '\|', 2
    $files = Get-ChildItem -LiteralPath $parts[1] -File | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' }
    if (-not $files) { throw "Missing license for $($parts[0])" }
    $sections.Add("## $($parts[0])")
    foreach ($file in $files) {
        $sections.Add("### $($file.Name)`n`n" + '```text' + "`n" + [IO.File]::ReadAllText($file.FullName) + "`n" + '```')
    }
}
[IO.File]::WriteAllText((Join-Path $PSScriptRoot '..\THIRD_PARTY_NOTICES.md'), ($sections -join "`n`n"), [Text.UTF8Encoding]::new($false))
