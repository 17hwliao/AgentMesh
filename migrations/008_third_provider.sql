ALTER TABLE tenant_model_routes DROP CHECK chk_tenant_model_routes_provider;
ALTER TABLE tenant_model_routes ADD CONSTRAINT chk_tenant_model_routes_provider_v2 CHECK (provider IN ('mock','ark','ollama','compatible'));
