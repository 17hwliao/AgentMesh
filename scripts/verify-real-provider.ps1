$preflight = & go run ./cmd/provider-verify 2>$null
$preflightExit = $LASTEXITCODE
if ($preflightExit -ne 0) {
    $preflight
    exit 1
}

$variables = @(
    'AGENTMESH_BOOTSTRAP_API_KEY', 'AGENTMESH_BOOTSTRAP_TENANT_ID', 'AGENTMESH_BOOTSTRAP_MODEL_ROUTES',
    'AGENTMESH_API_KEY', 'AGENTMESH_AUTH_STORE', 'AGENTMESH_AUTH_MYSQL_DSN', 'AGENTMESH_ADMIN_TOKEN',
    'AGENTMESH_QUOTA_MODE', 'AGENTMESH_QUOTA_MYSQL_DSN', 'AGENTMESH_QUOTA_REDIS_URL'
)
$saved = @{}
foreach ($name in $variables) {
    $entry = Get-Item "Env:$name" -ErrorAction SilentlyContinue
    $saved[$name] = @{ Exists = ($null -ne $entry); Value = if ($null -eq $entry) { '' } else { $entry.Value } }
}

$keyBytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($keyBytes)
$rng.Dispose()
$temporaryKey = -join ($keyBytes | ForEach-Object { $_.ToString('x2') })
$temporaryRoot = Join-Path $env:TEMP 'agentmesh-real-provider-verify'
$apiBinary = "$temporaryRoot-api.exe"
$stdoutLog = "$temporaryRoot.out"
$stderrLog = "$temporaryRoot.err"

try {
    Remove-Item -LiteralPath $apiBinary, $stdoutLog, $stderrLog -Force -ErrorAction SilentlyContinue
    $env:AGENTMESH_BOOTSTRAP_API_KEY = $temporaryKey
    $env:AGENTMESH_BOOTSTRAP_TENANT_ID = 'provider_verify'
    $env:AGENTMESH_BOOTSTRAP_MODEL_ROUTES = '{"provider-evidence":["ark","ollama"]}'
    $env:AGENTMESH_API_KEY = $temporaryKey
    Remove-Item Env:AGENTMESH_AUTH_STORE, Env:AGENTMESH_AUTH_MYSQL_DSN, Env:AGENTMESH_ADMIN_TOKEN -ErrorAction SilentlyContinue
    Remove-Item Env:AGENTMESH_QUOTA_MODE, Env:AGENTMESH_QUOTA_MYSQL_DSN, Env:AGENTMESH_QUOTA_REDIS_URL -ErrorAction SilentlyContinue

    & go build -o $apiBinary ./cmd/api
    if ($LASTEXITCODE -ne 0) { throw 'provider verification gateway build failed' }
    $process = Start-Process -FilePath $apiBinary -ArgumentList @('--addr', '127.0.0.1:18083', '--providers', 'ark,ollama') -WorkingDirectory (Get-Location).Path -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    $ready = $false
    for ($index = 0; $index -lt 40; $index++) {
        Start-Sleep -Milliseconds 250
        try {
            if ((Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18083/healthz' -TimeoutSec 1).StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {}
    }
    if (-not $ready) { throw 'provider verification gateway unavailable' }

    $body = '{"model":"provider-evidence","messages":[{"role":"user","content":"Reply with one short acknowledgement."}],"stream":true}'
    $chat = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18083/v1/chat/completions' -Method Post -Headers @{ Authorization = "Bearer $temporaryKey" } -ContentType 'application/json' -Body $body -TimeoutSec 45
    $traceID = $chat.Headers['X-AgentMesh-Trace-ID']
    if ([string]::IsNullOrWhiteSpace($traceID)) { throw 'provider verification trace missing' }
    $trace = (Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:18083/v1/observability/traces/$traceID" -Headers @{ Authorization = "Bearer $temporaryKey" } -TimeoutSec 5).Content | ConvertFrom-Json
    [ordered]@{
        status = 'verification_completed'
        code = $trace.result_code
        network_attempts = @($trace.attempts).Count
        provider_attempts = @($trace.attempts).Count
        providers = @($trace.attempts | ForEach-Object { $_.provider })
    } | ConvertTo-Json -Compress
} catch {
    [ordered]@{
        status = 'verification_failed'
        code = 'provider_verification_failed'
        network_attempts = $null
        provider_attempts = $null
    } | ConvertTo-Json -Compress
    exit 1
} finally {
    if ($process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force
        $process.WaitForExit()
    }
    Remove-Item -LiteralPath $apiBinary, $stdoutLog, $stderrLog -Force -ErrorAction SilentlyContinue
    foreach ($name in $variables) {
        if ($saved[$name].Exists) { Set-Item "Env:$name" $saved[$name].Value } else { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
    }
}
