# 后端扩展运行契约（2026-10-10）

本文件补充 backend-expansion.md。Ark、Ollama、OpenAI 兼容平台三种 provider；兼容平台通过 COMPATIBLE_BASE_URL / COMPATIBLE_MODELS（alias→实际 model 的 JSON）及私有 COMPATIBLE_API_KEY_FILE 配置。当前国内模型示例采用千问；其他兼容平台必须先核对流式 usage、实际模型标识和价格，不能仅凭榜单名称启用计费。

千问初次安装的联合配置见同级 `deploy/QWEN-RUNBOOK.md`。Base URL 按账户地域与业务空间填写，API key 只在服务端保存。非思考模式设置 `COMPATIBLE_DISABLE_THINKING=true`；支持新输出参数的模型可设置 `COMPATIBLE_USE_MAX_COMPLETION_TOKENS=true`，否则仍发送 `max_tokens`。这两个控制值只接受 true/false，拼写错误会拒绝启动。最终流式用量必须包含输入和输出字段，重复/非法用量会失败；空角色、结束或推理块不会当作回答转发。真实官方调用、价格与云端验收仍须账户资料。

停用已知真实provider时，持久租户路由不再使整个网关启动失败。任何包含停用provider的路由整体返回403，不删减、不换序、不自动转到其他模型；其他有效路由继续服务。未知provider、重复或非法活动顺序仍拒绝启动。管理员只能新建包含当前活动provider的路由。切换配置后须重建gateway；收费路由的价格/执行器绑定校验继续生效。

## 余额与账单

配置 AGENTMESH_BILLING_CATALOG_FILE 后启用独立人民币钱包，一元等于 1,000,000,000 nano。原 token/budget-unit 配额不会转换为人民币。新钱包余额为零。价格表字段见联合部署 billing-catalog.example.json；报价按 provider、公开 alias、实际 upstream_model、最大输入 token 绑定，带来源与到期时间，canonical SHA 为不可变版本。

金额为 `(非缓存输入×输入价 + 缓存输入×缓存价 + 输出×输出价)/1,000,000`，在 nano 精度最后向上取整一次；未知 cached 价按普通输入价。按供应商实际、非 estimated usage 结算，不用字符估算制造账单。请求先预留最多三次供应商尝试的费用上界，成功后释放剩余金额；已产生实际 usage 的失败尝试也有成本。输入采用保守字节上界拒绝超长请求，输出最多4096 token。价格目录支持最多96项；价格必须覆盖可用收费路由并与执行器实际模型对应，超出上下文/思考模式适用档位应拒绝请求或换独立 alias/价格表。

没有可确认 usage 的已启动调用进入 pending_reconciliation，保留 hold、charged=0。这不表示供应商免费，也不能自动退还。后台每15秒恢复老请求；未启动的请求可取消，已记录用量可结算，不确定用量留待人工提供上游凭证。目录过期停止收费服务 readiness；修改挂载价格文件后重建 gateway，旧请求仍使用原版目录。

租户接口：GET /v1/billing/balance、/prices、/requests（最近100条）；流式响应 X-AgentMesh-Billing-Request-ID 用于关联账单。仅私网、独立 admin Bearer 可调用：

```json
// POST /admin/billing/credits；credit_id 全局唯一，同请求重放成功，冲突409
{"tenant_id":"customer","credit_id":"verified-receipt-001","amount_nano":1000000000,"proof":"真实收款凭证或明确管理赠额依据"}
// POST /admin/billing/resolve；不得覆盖已记录的实际用量
{"request_id":"实际请求ID","proof":"供应商账单凭证","attempts":[{"ordinal":1,"usage":{"input_tokens":100,"output_tokens":50,"cached_input_tokens":0,"estimated":false}}]}
```

管理充值、补账和金额修改均持久审计。未选择支付商户/供应商，因此没有伪造支付回调、线上收款或自动充值；后续支付接入须验证签名、金额币种、收款方、订单及幂等，再调用同一信用记账事务。不要把 acceptance 的管理赠额当真实收款。

## 语义缓存

全局 AGENTMESH_SEMANTIC_CACHE=true 后，客户端仍须显式提交 X-AgentMesh-Cache: semantic 和 X-AgentMesh-Cache-Namespace: 知识/业务版本。仅 temperature=0、单 user 或 system+user 适用；租户、模型、路由、provider配置、embedding模型、输出上限、完整system及namespace共同隔离。有限向量、阈值≥0.995、数字序列匹配、最多128条/租户、默认10分钟TTL。只缓存完整已知用量的成功回答；Redis/embedding失败回到供应商路径。

缓存命中不会再次请求聊天供应商，新增聊天费用为0，账单状态cache_hit。缓存 embedding 当前由本地 Ollama 提供；embeddings 仍走原预算体系，不属于新的精确人民币收费接口。语义相似不证明问题等价，不能用于SQL审批、转账、实时库存等要求严格一致的流程；这些客户端不要开启缓存。修改知识或模型配置时换 namespace/SEMANTIC_CACHE_VERSION。

## 独立模型执行器

model-runner 单独持有供应商凭据，gateway 只持控制库/钱包及 mTLS 调用资料。双方校验内部 CA、证书/SAN、至少32字符service token；TLS≥1.3，128KiB消息、32并发、2分钟流期限，取消沿调用链传播。Health验证传输，不发收费探测。启动时比对执行器实际 model 与价格；每次RPC绑定 expected_upstream_model，返回流必须匹配并明确completed，截断EOF失败。

默认Linux部署仍为单机Compose；可选配置、Kubernetes结构验证、证书轮换、价格续期、余额开通和备份见联合目录 deploy/ROADMAP-RUNBOOK.md。真实模型质量、实时可用性和正式收费单必须由目标账户联调。
