$demo = & go test -count=1 -run TestStage4DemoScenario -v ./internal/reservation 2>&1
if ($LASTEXITCODE -ne 0) { throw "stage-4 deterministic demo failed: $($demo -join ' ')" }
$demo
'storage_mode=deterministic in-process doubles; no MySQL/Redis endpoint or Docker was contacted'
