param(
  [Parameter(Mandatory = $true)]
  [ValidateSet('Install', 'Start', 'Stop')]
  [string]$Action,
  [Parameter(Mandatory = $true)]
  [string]$Root,
  [string]$StagedPath,
  [string]$ApiURL
)

$ErrorActionPreference = 'Stop'

function Get-NormalizedPath([string]$Path) {
  return [IO.Path]::GetFullPath($Path)
}

function Get-ProcessPath($Process) {
  try {
    return $Process.Path
  } catch {
    return $null
  }
}

function Get-ExpectedAgentProcesses([string]$ExpectedPath) {
  $result = @()
  foreach ($process in @(Get-Process -Name 'nerve-agent' -ErrorAction SilentlyContinue)) {
    $processPath = Get-ProcessPath $process
    if (-not [string]::IsNullOrWhiteSpace($processPath) -and (Get-NormalizedPath $processPath) -ieq $ExpectedPath) {
      $result += $process
    }
  }
  return $result
}

function Stop-ExpectedAgentProcesses([string]$ExpectedPath) {
  $matches = @(Get-ExpectedAgentProcesses $ExpectedPath)
  if ($matches.Count -eq 0) {
    Write-Host '[INFO] Kein passender laufender Discovery-Agent gefunden.'
    return
  }

  foreach ($process in $matches) {
    if (-not $process.HasExited) {
      Write-Host ('[INFO] Beende Discovery-Agent PID ' + $process.Id + '.')
      Stop-Process -Id $process.Id -Force
    }
  }

  $deadline = [DateTime]::UtcNow.AddSeconds(8)
  do {
    Start-Sleep -Milliseconds 150
    $remaining = @(Get-ExpectedAgentProcesses $ExpectedPath)
  } while ($remaining.Count -gt 0 -and [DateTime]::UtcNow -lt $deadline)

  if ($remaining.Count -gt 0) {
    throw 'Mindestens ein passender Discovery-Agent konnte nicht innerhalb von 8 Sekunden beendet werden.'
  }
  Write-Host '[OK] Alle passenden Discovery-Agent-Prozesse wurden beendet.' -ForegroundColor Green
}

function Clear-AgentPidFile([string]$PidPath, [string]$ExpectedPath) {
  if (-not (Test-Path -LiteralPath $PidPath)) {
    return
  }

  $agentPid = 0
  $content = [IO.File]::ReadAllText($PidPath).Trim()
  if (-not [int]::TryParse($content, [ref]$agentPid) -or $agentPid -le 0) {
    Write-Host '[WARNUNG] Ungueltige Agent-PID-Datei wird entfernt.' -ForegroundColor Yellow
  } else {
    $process = Get-Process -Id $agentPid -ErrorAction SilentlyContinue
    if ($null -eq $process) {
      Write-Host '[INFO] Veraltete Agent-PID-Datei wird entfernt.'
    } else {
      $processPath = Get-ProcessPath $process
      if ([string]::IsNullOrWhiteSpace($processPath) -or (Get-NormalizedPath $processPath) -ine $ExpectedPath) {
        Write-Host '[WARNUNG] Die gespeicherte PID gehoert nicht zum NERVE-Agenten. Der fremde Prozess bleibt unangetastet.' -ForegroundColor Yellow
      } else {
        Write-Host '[INFO] Registrierte Agent-PID-Datei wird bereinigt.'
      }
    }
  }
  Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
}

function Install-Agent([string]$ExpectedPath, [string]$PidPath, [string]$SourcePath) {
  if ([string]::IsNullOrWhiteSpace($SourcePath) -or -not (Test-Path -LiteralPath $SourcePath -PathType Leaf)) {
    throw 'Das gebaute Discovery-Agent-Artefakt fehlt.'
  }
  [void](New-Item -ItemType Directory -Path (Split-Path -Parent $ExpectedPath) -Force)
  Clear-AgentPidFile $PidPath $ExpectedPath
  Stop-ExpectedAgentProcesses $ExpectedPath

  $lastError = $null
  foreach ($attempt in 1..8) {
    try {
      Copy-Item -LiteralPath $SourcePath -Destination $ExpectedPath -Force
      Write-Host '[OK] Discovery-Agent wurde installiert.' -ForegroundColor Green
      return
    } catch {
      $lastError = $_
      if ($attempt -lt 8) {
        Start-Sleep -Milliseconds 300
      }
    }
  }
  throw ('Der Discovery-Agent konnte nach 8 Versuchen nicht ersetzt werden: ' + $lastError.Exception.Message)
}

function Start-Agent([string]$ExpectedPath, [string]$PidPath, [string]$ProjectRoot, [string]$Endpoint) {
  if ([string]::IsNullOrWhiteSpace($Endpoint)) {
    throw 'Die API-Adresse fuer den Discovery-Agenten fehlt.'
  }
  if (-not (Test-Path -LiteralPath $ExpectedPath -PathType Leaf)) {
    throw 'Der installierte Discovery-Agent fehlt.'
  }
  Clear-AgentPidFile $PidPath $ExpectedPath
  Stop-ExpectedAgentProcesses $ExpectedPath

  $values = @{}
  foreach ($line in [IO.File]::ReadAllLines((Join-Path $ProjectRoot '.env'))) {
    if ($line -match '^\s*#' -or $line -notmatch '=') {
      continue
    }
    $parts = $line -split '=', 2
    $values[$parts[0].Trim()] = $parts[1].Trim().Trim([char]34).Trim([char]39)
  }
  if ([string]::IsNullOrWhiteSpace($values['NERVE_DISCOVERY_AGENT_TOKEN'])) {
    throw 'NERVE_DISCOVERY_AGENT_TOKEN fehlt in .env.'
  }

  $env:NERVE_DISCOVERY_AGENT_TOKEN = $values['NERVE_DISCOVERY_AGENT_TOKEN']
  $env:NERVE_AGENT_API_URL = $Endpoint
  $logsPath = Join-Path $ProjectRoot '.nerve\logs'
  [void](New-Item -ItemType Directory -Path $logsPath -Force)
  $process = Start-Process -FilePath $ExpectedPath -WorkingDirectory $ProjectRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $logsPath 'discovery-agent.log') -RedirectStandardError (Join-Path $logsPath 'discovery-agent-error.log') -PassThru
  Start-Sleep -Milliseconds 750
  if ($process.HasExited) {
    throw 'Der lokale Discovery-Agent wurde unmittelbar nach dem Start beendet.'
  }
  [IO.File]::WriteAllText($PidPath, [string]$process.Id)
  Write-Host '[OK] Discovery-Agent wurde gestartet.' -ForegroundColor Green
}

$projectRoot = Get-NormalizedPath $Root
$expectedPath = Get-NormalizedPath (Join-Path $projectRoot '.nerve\agent\nerve-agent.exe')
$pidPath = Join-Path $projectRoot '.nerve\agent.pid'

switch ($Action) {
  'Install' {
    Install-Agent $expectedPath $pidPath (Get-NormalizedPath $StagedPath)
  }
  'Start' {
    Start-Agent $expectedPath $pidPath $projectRoot $ApiURL
  }
  'Stop' {
    Clear-AgentPidFile $pidPath $expectedPath
    Stop-ExpectedAgentProcesses $expectedPath
  }
}
