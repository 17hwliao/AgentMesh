.PHONY: demo-stage-1 demo-stage-4 verify-real-storage

demo-stage-1:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-stage-1.ps1

demo-stage-4:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-stage-4.ps1

verify-real-storage:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify-real-storage.ps1
