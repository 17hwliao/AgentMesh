param(
    [int]$TimeoutSeconds = 75
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot

$apiKey = 'local-demo-api-key-123456'
$endpoint = 'http://127.0.0.1:18080'
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)

while ([DateTime]::UtcNow -lt $deadline) {
    try {
        $health = Invoke-WebRequest -UseBasicParsing -Uri "$endpoint/readyz" -TimeoutSec 2
        if ($health.StatusCode -eq 200) { break }
    } catch {}
    Start-Sleep -Milliseconds 500
}
if (-not $health -or $health.StatusCode -ne 200) {
    throw 'API readiness check did not succeed'
}

$body = '{"model":"mock-model","messages":[{"role":"user","content":"reference runtime verification"}],"stream":true}'
$response = Invoke-WebRequest -UseBasicParsing -Uri "$endpoint/v1/chat/completions" -Method Post -Headers @{ Authorization = "Bearer $apiKey" } -ContentType 'application/json' -Body $body -TimeoutSec 15
if ($response.StatusCode -ne 200) {
    throw "chat request status=$($response.StatusCode)"
}

function Query-Count([string]$sql) {
    $value = & docker compose -f compose.yml exec -T mysql mysql -uroot -pagentmesh-local-only -Nse $sql agentmesh_control
    if ($LASTEXITCODE -ne 0) { throw "mysql query failed: $sql" }
    return [int]($value | Select-Object -First 1)
}

$projectionCount = 0
$publishedCount = 0
while ([DateTime]::UtcNow -lt $deadline) {
    $publishedCount = Query-Count 'SELECT COUNT(*) FROM usage_kafka_outbox WHERE published_at IS NOT NULL'
    $projectionCount = Query-Count 'SELECT COUNT(*) FROM usage_kafka_projections'
    if ($publishedCount -gt 0 -and $projectionCount -gt 0) { break }
    Start-Sleep -Milliseconds 500
}
if ($publishedCount -lt 1 -or $projectionCount -lt 1) {
    throw "usage pipeline did not converge: published=$publishedCount projections=$projectionCount"
}

[pscustomobject]@{
    Status           = 'verified'
    APIStatus        = $response.StatusCode
    PublishedOutbox  = $publishedCount
    UsageProjections = $projectionCount
} | ConvertTo-Json -Compress
