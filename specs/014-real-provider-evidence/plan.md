# 014 计划：受控真实 Provider 联调

## 可测试核心

新增 `internal/providerverify` 的纯配置加载与安全报告类型。它只读取五个既有 Provider 环境变量，绝不序列化其值；缺失或无效时返回 `verification_unavailable/provider_configuration_missing` 或 `provider_configuration_invalid`，Provider 网络尝试固定为 0。`cmd/provider-verify` 只输出该预检 JSON，不能自行启动 Provider 请求。

## 受控运行入口

新增 `scripts/verify-real-provider.ps1` 与 Make target。脚本先运行预检；仅完整配置才临时生成 Bootstrap/API Key、清除持久 auth/quota mode、以 `--providers ark,ollama` 启动回环 API，并通过现有 HTTP client 发送一条固定 stream 请求。输出/临时文件只保存安全状态、尝试数、稳定码与 trace 摘要；模型 delta、原始错误、URL、模型名及任何 Key 不进入摘要。

脚本以 try/finally 停止子进程、删除临时 binary/log，并精确恢复它改写的环境变量。它只允许一个请求；Router 自身维持 Ark primary 与首块前 Ollama fallback，首块后不切换。超时、启动失败、stream 失败都以失败摘要结束，不重跑或扩大请求数。

## 测试与运行事实

- T001：纯 Go 测试预检缺失/有效/无泄露及 JSON 摘要；验证预检本身无网络入口。
- T002：实现 PowerShell 编排与 Make target；脚本静态/离线测试确认回环、固定请求、finally 恢复和输出字段，不记录凭据。
- T003：运行缺配置的真实入口并记录实际受控拒绝；仅操作者已配置五项变量时再运行一次真实单请求，按真实成功或失败写入事实。
- T004：README 分列已有离线、受控拒绝和真实结果；全量验证、Adaptive、私有复盘、提交并 fast-forward `master`；不 push、不 tag、不启动 Docker。

章程：暂未建立；本计划不阻塞执行。
