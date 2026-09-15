# Shared by the local and optional sensor setup; dot-source as a real file.
function Get-Value([string]$Key) {
    $value = ''
    foreach ($line in $lines) { if ($line -match ('^\s*' + [regex]::Escape($Key) + '\s*=(.*)$')) { $value = $Matches[1].Trim() } }
    if ($value.StartsWith("'") -and $value.EndsWith("'")) { return $value.Substring(1,$value.Length-2).Replace("\'", "'") }
    if ($value.StartsWith('"') -and $value.EndsWith('"')) { return $value.Substring(1,$value.Length-2) }
    return $value
}
function Set-Value([string]$Key, [string]$Value, [switch]$Literal) {
    if ($Value -match '[\r\n\x00]') { throw 'Configuration values must be single-line.' }
    if ($Literal) { $Value = "'" + $Value.Replace("'", "\'") + "'" }
    $found = $false
    for ($i=0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match ('^\s*' + [regex]::Escape($Key) + '\s*=')) { $lines[$i] = $Key + '=' + $Value; $found = $true }
    }
    if (-not $found) { $lines.Add($Key + '=' + $Value) }
}
function New-Secret {
    $bytes = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant()
}
function Save-SetupEnvironment {
    # Atomic replacement prevents a truncated .env on interruption.
    $temporary = $envPath + '.setup.tmp'
    try {
        [IO.File]::WriteAllText($temporary, (($lines -join "`r`n") + "`r`n"), $utf8)
        if (Test-Path -LiteralPath $envPath) { [IO.File]::Replace($temporary,$envPath,[NullString]::Value) }
        else { [IO.File]::Move($temporary,$envPath) }
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary } }
}
