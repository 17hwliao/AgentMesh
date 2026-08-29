.PHONY: demo-stage-1 demo-stage-4

demo-stage-1:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-stage-1.ps1

demo-stage-4:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-stage-4.ps1
