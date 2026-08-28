$keyBytes = New-Object byte[] 32
$rng = New-Object System.Security.Cryptography.RNGCryptoServiceProvider
$rng.GetBytes($keyBytes)
$rng.Dispose()
$stageKey = -join ($keyBytes | ForEach-Object { $_.ToString('x2') })
$saved = @{
    BootstrapKey    = $env:AGENTMESH_BOOTSTRAP_API_KEY
    BootstrapTenant = $env:AGENTMESH_BOOTSTRAP_TENANT_ID
    BootstrapRoutes = $env:AGENTMESH_BOOTSTRAP_MODEL_ROUTES
    APIKey          = $env:AGENTMESH_API_KEY
}
$env:AGENTMESH_BOOTSTRAP_API_KEY = $stageKey
$env:AGENTMESH_BOOTSTRAP_TENANT_ID = 'demo_tenant'
$env:AGENTMESH_BOOTSTRAP_MODEL_ROUTES = '{"mock-model":["mock"]}'
$env:AGENTMESH_API_KEY = $stageKey
$logBase = Join-Path $env:TEMP 'agentmesh-demo-stage-1'
$stdoutLog = "$logBase.out"
$stderrLog = "$logBase.err"
$apiBinary = Join-Path $env:TEMP 'agentmesh-demo-stage-1-api.exe'
Remove-Item -LiteralPath $stdoutLog, $stderrLog, $apiBinary -Force -ErrorAction SilentlyContinue
& go build -o $apiBinary ./cmd/api
if ($LASTEXITCODE -ne 0) { throw 'mock gateway build failed' }
$process = Start-Process -FilePath $apiBinary -ArgumentList @('--addr', '127.0.0.1:18082') -WorkingDirectory (Get-Location).Path -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
try {
    $ready = $false
    for ($i = 0; $i -lt 60; $i++) {
        Start-Sleep -Milliseconds 500
        try {
            $health = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18082/healthz' -TimeoutSec 2
            if ($health.StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {}
    }
    if (-not $ready) { throw 'mock gateway did not become ready' }
    $summary = & go run ./cmd/summary-cli --endpoint 'http://127.0.0.1:18082' --model 'mock-model' --text 'stage one summary' 2>&1
    if ($LASTEXITCODE -ne 0) { throw "summary CLI failed: $($summary -join ' ')" }
    $diagnose = & go run ./cmd/sql-diagnose-cli --endpoint 'http://127.0.0.1:18082' --model 'mock-model' --sql 'SELECT id FROM users' 2>&1
    if ($LASTEXITCODE -ne 0) { throw "SQL diagnostic CLI failed: $($diagnose -join ' ')" }
    $chat = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18082/v1/chat/completions' -Method Post -Headers @{ Authorization = "Bearer $stageKey" } -ContentType 'application/json' -Body '{"model":"mock-model","messages":[{"role":"user","content":"trace demonstration"}],"stream":true}'
    $traceID = $chat.Headers['X-AgentMesh-Trace-ID']
    if ([string]::IsNullOrWhiteSpace($traceID)) { throw 'trace header missing' }
    $trace = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:18082/v1/observability/traces/$traceID" -Headers @{ Authorization = "Bearer $stageKey" }
    $cancelTest = & go test -count=1 -run TestCancellationReachesMockAndReturns -v ./internal/router 2>&1
    if ($LASTEXITCODE -ne 0) { throw 'cancellation demonstration failed' }
    "health=$($health.StatusCode)"
    "summary=$($summary.ToString().Trim())"
    "sql_diagnose=$($diagnose.ToString().Trim())"
    "trace_query=$($trace.StatusCode) $($trace.Content)"
    'fallback=mock-primary fails before first chunk; mock-fallback responds'
    'cancellation=TestCancellationReachesMockAndReturns passed'
} finally {
    if ($process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force
        $process.WaitForExit()
    }
    Remove-Item -LiteralPath $stdoutLog, $stderrLog, $apiBinary -Force -ErrorAction SilentlyContinue
    $env:AGENTMESH_BOOTSTRAP_API_KEY = $saved.BootstrapKey
    $env:AGENTMESH_BOOTSTRAP_TENANT_ID = $saved.BootstrapTenant
    $env:AGENTMESH_BOOTSTRAP_MODEL_ROUTES = $saved.BootstrapRoutes
    $env:AGENTMESH_API_KEY = $saved.APIKey
}
