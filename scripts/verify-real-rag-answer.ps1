# Opt-in smoke test. It intentionally does nothing unless an operator confirms
# a non-production Ollama endpoint and model through process environment.
if ($env:AGENTMESH_REAL_RAG_ANSWER_VALIDATION -ne '1') {
    '{"status":"verification_unavailable","code":"real_rag_answer_validation_not_enabled","network_attempts":0}'
    exit 1
}
foreach ($name in 'AGENTMESH_RAG_ANSWER_BASE_URL','AGENTMESH_RAG_ANSWER_MODEL') {
    if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name))) {
        '{"status":"verification_unavailable","code":"rag_answer_provider_configuration_missing","network_attempts":0}'
        exit 1
    }
}

$key = 'rag-verify-key-0123456789'
$binary = Join-Path ([IO.Path]::GetTempPath()) ("agentmesh-rag-verify-" + [guid]::NewGuid().ToString('N') + '.exe')
$saved = @{}
foreach ($name in 'AGENTMESH_BOOTSTRAP_API_KEY','AGENTMESH_BOOTSTRAP_TENANT_ID','AGENTMESH_BOOTSTRAP_MODEL_ROUTES','AGENTMESH_RAG_ANSWER_PROVIDER','OLLAMA_BASE_URL','OLLAMA_MODEL') {
    $item = Get-Item "Env:$name" -ErrorAction SilentlyContinue
    $saved[$name] = @{ Exists = ($null -ne $item); Value = if ($null -eq $item) { '' } else { $item.Value } }
}
try {
    $env:AGENTMESH_BOOTSTRAP_API_KEY = $key
    $env:AGENTMESH_BOOTSTRAP_TENANT_ID = 'rag_verify'
    $env:AGENTMESH_BOOTSTRAP_MODEL_ROUTES = '{"rag-verify":["ollama"]}'
    $env:AGENTMESH_RAG_ANSWER_PROVIDER = 'ollama'
    # The existing chat provider selection is needed solely for the shared
    # tenant resolver. Keep its Ollama config equal to this explicitly chosen
    # answer endpoint/model; neither value is printed.
    $env:OLLAMA_BASE_URL = $env:AGENTMESH_RAG_ANSWER_BASE_URL
    $env:OLLAMA_MODEL = $env:AGENTMESH_RAG_ANSWER_MODEL
    & go build -buildvcs=false -o $binary ./cmd/api
    if ($LASTEXITCODE -ne 0) { throw 'gateway_build_failed' }
    $server = Start-Process -FilePath $binary -ArgumentList @('--addr','127.0.0.1:18084','--providers','ollama') -WorkingDirectory (Get-Location).Path -WindowStyle Hidden -PassThru
    $ready = $false
    for ($i = 0; $i -lt 40; $i++) { Start-Sleep -Milliseconds 250; try { if ((Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18084/healthz' -TimeoutSec 1).StatusCode -eq 200) { $ready = $true; break } } catch {} }
    if (-not $ready) { throw 'gateway_unavailable' }
    $content = 'CandidateSpec is retrieved test evidence.'
    $hash = ([System.Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($content)) | ForEach-Object { $_.ToString('x2') }) -join ''
    $body = @{ model='rag-verify'; question='What is this evidence?'; contexts=@(@{source_uri='verify://local';document_id='verify';version='v1';chunk_id='c1';hash=$hash;content=$content}) } | ConvertTo-Json -Depth 5 -Compress
    $response = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18084/v1/rag/answers' -Method Post -Headers @{Authorization="Bearer $key"} -ContentType 'application/json' -Body $body -TimeoutSec 30
    $decoded = $response.Content | ConvertFrom-Json
    if ($response.StatusCode -ne 200 -or (@($decoded.facts).Count -lt 1) -or (@($decoded.facts[0].citations).Count -lt 1)) { throw 'grounded_answer_contract_failed' }
    '{"status":"verification_completed","code":"success","network_attempts":1}'
} catch {
    '{"status":"verification_failed","code":"rag_answer_verification_failed"}'
    exit 1
} finally {
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force; $server.WaitForExit() }
    if (Test-Path -LiteralPath $binary) { Remove-Item -LiteralPath $binary -Force }
    foreach ($name in $saved.Keys) { if ($saved[$name].Exists) { Set-Item "Env:$name" $saved[$name].Value } else { Remove-Item "Env:$name" -ErrorAction SilentlyContinue } }
}
