# 联合后端部署模板

该目录保存两个项目的 Compose、千问接入、迁移/运维、验收与可选扩展模板。运行前把模板导出到两个源码目录同级的新部署目录，保留既有配置的相对路径。

```sh
git clone https://github.com/17hwliao/AgentMesh.git
git clone https://github.com/17hwliao/sql-sentinel.git
cd AgentMesh
python scripts/export-backend-stack.py --out ../deploy
cd ../deploy
```

目录结构为 `AgentMesh/`、`sql-sentinel/`、`deploy/` 同级。导出工具拒绝覆盖已有部署目录。随后按 [千问接入手册](QWEN-RUNBOOK.md)、[完整上线手册](ROADMAP-RUNBOOK.md) 和 [基础运行说明](README.md) 生成自己的私有配置并验收。生产 key 不放进命令行或源码。

本目录不含运行 .env、secrets、数据库备份、验收结果或已生成 Kubernetes Secret 清单；这些在独立的部署目录生成。静态模板本身不代表真实模型、云服务器或集群验收。使用 `extensions.yml` 时须显式启用 `--profile extensions`。
