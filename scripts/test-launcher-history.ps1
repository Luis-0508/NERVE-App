$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'setup-launchers.ps1')
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $repo ('.nerve/launcher history ' + [Guid]::NewGuid().ToString('N'))
[void](New-Item -ItemType Directory -Path $fixture)
foreach ($name in @('start','stop')) { Copy-Item (Join-Path $repo ($name + '.example.bat')) $fixture }
$catalog = Get-Content (Join-Path $PSScriptRoot 'stock-launchers.json') -Raw | ConvertFrom-Json
foreach ($entry in $catalog.entries) {
    $content = git -C $repo show ($entry.commit + ':' + $entry.path)
    if ($LASTEXITCODE -ne 0) { throw 'Historical source unavailable; full history required for this test.' }
    $text = ($content -join "`n") + "`n"
    if ((Get-LauncherHash $text) -ne $entry.sha256) { throw 'Catalog provenance mismatch.' }
    [IO.File]::WriteAllText((Join-Path $fixture ($entry.name + '.bat')), $text)
    Update-SetupLaunchers $fixture
    if ((Get-LauncherHash ([IO.File]::ReadAllText((Join-Path $fixture ($entry.name + '.bat'))))) -ne (Get-LauncherHash ([IO.File]::ReadAllText((Join-Path $fixture ($entry.name + '.example.bat')))))) { throw 'Historical migration failed.' }
}
# A recognizable banner is not permission to replace edited content, even without metadata.
[IO.File]::WriteAllText((Join-Path $fixture '.nerve/launchers.json'), '{broken')
$custom = [IO.File]::ReadAllText((Join-Path $fixture 'start.example.bat')) + "`necho Custom behavior`n"
[IO.File]::WriteAllText((Join-Path $fixture 'start.bat'), $custom)
Update-SetupLaunchers $fixture
if ([IO.File]::ReadAllText((Join-Path $fixture 'start.bat')) -cne $custom) { throw 'Invalid metadata overwrote custom launcher.' }
Write-Host ('PASS: all ' + $catalog.entries.Count + ' historical official contents/provenance, LF normalization, invalid metadata and edited stock preservation.')
