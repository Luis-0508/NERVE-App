# Maintainer tool: run from a full repository history before releasing launcher changes.
# Runtime setup uses the bundled catalog and does not need Git or network access.
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'setup-launchers.ps1')
$repo = Split-Path $PSScriptRoot -Parent
if ((git -C $repo rev-parse --is-shallow-repository) -ne 'false') { throw 'Full Git history required.' }
$entries = @{}
$path = Join-Path $PSScriptRoot 'stock-launchers.json'
if (Test-Path $path) {
    foreach ($entry in (Get-Content $path -Raw | ConvertFrom-Json).entries) { $entries[$entry.name + ':' + $entry.sha256] = $entry }
}
foreach ($name in @('start','stop')) {
    foreach ($file in @(($name + '.example.bat'), ($name + '.bat'))) {
        foreach ($commit in (git -C $repo log HEAD --diff-filter=AM --format=%H -- $file)) {
            $content = git -C $repo show ($commit + ':' + $file) 2>$null
            if ($LASTEXITCODE -ne 0) { continue }
            $hash = Get-LauncherHash ($content -join "`n")
            $entries[$name + ':' + $hash] = [ordered]@{name=$name; sha256=$hash; commit=$commit; path=$file}
        }
    }
}
[IO.File]::WriteAllText($path, ([ordered]@{schema=1; entries=@($entries.Values | Sort-Object { $_.name }, { $_.sha256 })} | ConvertTo-Json -Depth 5) + "`n", [Text.UTF8Encoding]::new($false))
