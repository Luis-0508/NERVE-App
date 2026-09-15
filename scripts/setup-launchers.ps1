# Exact content identity, never a banner/marker heuristic: edits remain custom.
function Get-LauncherHash([string]$Text) {
    $normalized = $Text.Replace("`r`n", "`n").TrimEnd("`n") + "`n"
    $sha = [Security.Cryptography.SHA256]::Create()
    try { [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($normalized))).Replace('-', '') }
    finally { $sha.Dispose() }
}

function Update-SetupLaunchers([string]$ProjectRoot) {
    $catalog = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'stock-launchers.json') -Raw | ConvertFrom-Json
    $stateDirectory = Join-Path $ProjectRoot '.nerve'
    $statePath = Join-Path $stateDirectory 'launchers.json'
    $previous = $null
    if (Test-Path -LiteralPath $statePath) {
        try { $previous = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json } catch {
            Write-Host '[INFO] Launcher-Metadaten unlesbar; pruefe offizielle Versionen.'
        }
    }
    $installed = [ordered]@{}
    foreach ($name in @('start', 'stop')) {
        $destination = Join-Path $ProjectRoot ($name + '.bat')
        $template = Join-Path $ProjectRoot ($name + '.example.bat')
        $targetHash = Get-LauncherHash ([IO.File]::ReadAllText($template))
        $existingHash = $null
        if (Test-Path -LiteralPath $destination) { $existingHash = Get-LauncherHash ([IO.File]::ReadAllText($destination)) }
        $managed = $previous -and $previous.schema -eq 1 -and $previous.installed.$name -eq $existingHash
        $known = @($catalog.entries | Where-Object { $_.name -eq $name -and $_.sha256 -eq $existingHash }).Count -gt 0
        if (-not $existingHash -or $existingHash -eq $targetHash -or $managed -or $known) {
            if ($existingHash -ne $targetHash) {
                Copy-Item -LiteralPath $template -Destination $destination -Force
                Write-Host ('[OK] ' + $name + '.bat auf aktuelle offizielle Version aktualisiert.')
            }
            $installed[$name] = $targetHash
        } else {
            Write-Host ('[INFO] Benutzerdefinierte ' + $name + '.bat erhalten. Aktuelle Vorlage: ' + $name + '.example.bat (Sensor-Start/-Stop ggf. manuell uebernehmen).')
        }
    }
    [void](New-Item -ItemType Directory -Path $stateDirectory -Force)
    $temporary = $statePath + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
    try {
        [IO.File]::WriteAllText($temporary, ([ordered]@{schema=1; installed=$installed} | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
        Move-Item -LiteralPath $temporary -Destination $statePath -Force
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary } }
}
