.PHONY: demo-stage-1 demo-stage-4 demo-admin verify-real-storage verify-real-provider

demo-stage-1:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-stage-1.ps1

demo-stage-4:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-stage-4.ps1

demo-admin:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-admin.ps1

verify-real-storage:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify-real-storage.ps1

verify-real-provider:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify-real-provider.ps1
