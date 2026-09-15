$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $repo ('.nerve/setup-tests-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
foreach ($file in @('.env.example','start.example.bat','stop.example.bat')) { Copy-Item -LiteralPath (Join-Path $repo $file) -Destination $fixture }
function Assert($Condition, $Message) { if (-not $Condition) { throw $Message } }
function EnvText { return [IO.File]::ReadAllText((Join-Path $fixture '.env')) }
function Value($Key) { return ([regex]::Match((EnvText), ('(?m)^' + $Key + '=(.*)')).Groups[1].Value.Trim()) }
& "$PSScriptRoot/setup.ps1" -Root $fixture -NonInteractive
$first = EnvText
$db = Value 'POSTGRES_PASSWORD'
$agent = Value 'NERVE_DISCOVERY_AGENT_TOKEN'
Assert ($db -match '^[a-f0-9]{64}$' -and $agent -match '^[a-f0-9]{64}$' -and $db -ne $agent) 'Fresh secrets are missing or reused.'
Assert ((Value 'NERVE_TRAFFIC_CAPTURE_MODE') -eq 'sensor-required') 'Fresh capture mode is dishonest.'
& "$PSScriptRoot/setup.ps1" -Root $fixture -NonInteractive
Assert ((EnvText) -ceq $first) 'Repeated setup changed existing configuration.'
Add-Content -LiteralPath (Join-Path $fixture '.env') -Value "# custom setting`r`nCUSTOM_SETTING='keep-me'`r`n"
$password = ConvertTo-SecureString 'test-only-$#with!quotes' -AsPlainText -Force
& "$PSScriptRoot/setup-traffic-sensor.ps1" -Root $fixture -NonInteractive -TrafficMode external -NtopngURL 'http://host.docker.internal:3001' -Username reader -Password $password -InterfaceID 7
Assert ((Value 'NERVE_NTOPNG_PASSWORD') -ceq "'test-only-`$#with!quotes'") 'External password was not preserved literally.'
Assert ((Value 'NERVE_NTOPNG_INTERFACE_ID') -eq '7') 'Explicit interface was overwritten.'
$external = EnvText
& "$PSScriptRoot/setup-traffic-sensor.ps1" -Root $fixture -NonInteractive
Assert ((EnvText) -ceq $external) 'Optional preserve mode changed the source.'
& "$PSScriptRoot/setup.ps1" -Root $fixture -NonInteractive
Assert ((EnvText) -ceq $external) 'Existing external source was changed.'
$failed = $false
try { & "$PSScriptRoot/setup-traffic-sensor.ps1" -Root $fixture -NonInteractive -TrafficMode external -NtopngURL 'http://user:password@invalid' -Username reader -Password $password } catch { $failed = $true }
Assert $failed 'Embedded credentials were accepted.'
Assert ((EnvText) -ceq $external) 'Failed setup changed .env.'
& "$PSScriptRoot/setup-traffic-sensor.ps1" -Root $fixture -NonInteractive -TrafficMode linux-interface -SensorAddress 192.0.2.50 -CaptureInterface enp3s0
$sensor = EnvText
& "$PSScriptRoot/setup.ps1" -Root $fixture
Assert ((EnvText) -ceq $sensor) 'Normal setup changed an existing Linux sensor configuration.'
$readerPath = Join-Path $fixture '.nerve/sensor/reader.password'
$reader = [IO.File]::ReadAllText($readerPath)
Assert ((Value 'NERVE_NTOPNG_PASSWORD') -ceq ("'" + $reader + "'")) 'API and sensor secrets differ.'
Assert ($reader -ne [IO.File]::ReadAllText((Join-Path $fixture '.nerve/sensor/admin.password'))) 'Admin and reader secrets must differ.'
& "$PSScriptRoot/setup-traffic-sensor.ps1" -Root $fixture -NonInteractive -TrafficMode linux-interface -SensorAddress 192.0.2.50 -CaptureInterface enp3s0
Assert ((EnvText) -ceq $sensor -and [IO.File]::ReadAllText($readerPath) -ceq $reader) 'Sensor rerun rotated credentials.'
Assert ((Value 'POSTGRES_PASSWORD') -ceq $db -and (Value 'NERVE_DISCOVERY_AGENT_TOKEN') -ceq $agent) 'Database or discovery credentials changed.'
Assert ((EnvText).Contains("CUSTOM_SETTING='keep-me'")) 'Custom configuration was lost.'
Write-Host 'PASS: fresh setup, rerun, external source, literal secrets, failed setup, sensor secret persistence, discovery token and custom values.'
# Keep ignored fixtures for Compose validation, without printing their contents.
