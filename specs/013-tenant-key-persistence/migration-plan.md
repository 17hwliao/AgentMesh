# 013 持久租户与 API Key 迁移计划

## 触发与前提

本批新增持久身份数据，命中 L3 migration 触发条件。迁移为 `migrations/003_tenant_api_keys.sql`，仅面向 MySQL 8 受控实例，由拥有最小 DDL 权限的操作者显式应用；应用前备份 schema，应用后才可启用 `AGENTMESH_AUTH_STORE=mysql`。

## Schema

- `tenants`：`tenant_id` 主键、`enabled`、创建/更新时间；不保存 prompt、请求、配额、凭据或外部端点。
- `tenant_model_routes`：tenant 外键、model、正整数 ordinal、provider；`(tenant_id, model, ordinal)` 保持 fallback 顺序，并唯一约束同一路由的 provider，限制名称为 `mock`、`ark`、`ollama`。
- `api_keys`：不可猜测 `key_id` 主键、tenant 外键、唯一 prefix、`BINARY(32)` SHA-256 digest、enabled、created/revoked 时间。原始 Key、admin token、DSN 绝不入表。

## 执行、兼容与回滚

迁移只能创建本批三表和必要索引，不能 ALTER/DROP 001/002 的配额或账务表；重跑必须安全地发现已存在的预期对象，形状不符即停止。部署时先迁移、再配置持久认证、最后启动服务；未启用 mysql mode 的既有 Bootstrap 内存 demo 不受影响。

一旦表中存有有效 tenant 或 Key 证据，不以 drop-table 作为常规回滚：关闭 `AGENTMESH_AUTH_STORE=mysql`、保留数据并恢复到内存 Bootstrap 模式。仅在确认零业务身份数据的开发环境，操作者才可人工删除这三张新表；迁移文件不提供自动 destructive down 操作。

## 验证

离线 SQL 夹具验证表、外键、唯一键、route 顺序约束与不含原始凭据；repository 测试验证创建后重启加载、撤销立即拒绝与 prefix 冲突重试。真实 MySQL 运行只在操作者单独配置非生产环境后执行，不在本批伪造成功实证。
