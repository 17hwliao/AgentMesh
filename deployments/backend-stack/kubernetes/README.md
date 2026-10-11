# Optional Kubernetes deployment

The supported primary deployment is Linux + Compose. Kubernetes is an explicit
single-replica deployment option; it does not add multi-node MySQL/Kafka HA.

Delivered JSON files are fixture validation samples, annotated
`fixture-only-do-not-deploy`. Regenerate from real configuration and registry
image names; the renderer requires explicit `--fixture-validation` to accept
fixture endpoints. Validate 48 base / 76 extended resources with official
Kubernetes API types; no actual cluster admission was performed.

1. Configure `setup.py` and `.env`, build the two images and transfer them to a
   registry accessible to the cluster. No registry push was performed here.
2. Run `python render-kubernetes.py --out kubernetes/release.json --am-image
   YOUR_REGISTRY/agentmesh:TAG --ss-image YOUR_REGISTRY/sql-sentinel:TAG
   --proxy-image YOUR_REGISTRY/backend-proxy:TAG`.
   Add `--extensions` only after preparing compatible model credentials, a
   valid price catalog, internal certificates and the expiring ES app key.
3. Inspect the generated resources. Supply a default StorageClass, a CNI that
   enforces NetworkPolicy, LoadBalancer support, DNS and adequate storage/RAM.
   Pin approved datastore image digests and operator-specific resource limits.
4. Create the namespace and install `backend-secrets` from the required release
   files with `kubectl create secret generic backend-secrets --from-file=...`.
   **Do not include `grpc_ca_key`.** Secret keys use Compose source names
   (`control_root`, `am_dsn`, etc.), not mount target names. Apps mount only their
   selected keys. Keep generated secret YAML outside Git/source packages.
5. Apply backing Service/PVC/StatefulSet/ConfigMap/NetworkPolicy resources first.
   Wait for MySQL, Redis and Kafka readiness, then apply `am-migrate`,
   `ss-migrate` and `topics` Jobs and wait for successful completion. Optional
   ES index/key bootstrap requires a verified local `kubectl port-forward`.
   Run `runner-grants` after `am-migrate` before starting `model-runner`.
6. Apply API, relay, worker and optional executor Deployments, then Caddy.
   Kubernetes does not implement Compose `depends_on`; following this order is
   necessary. Caddy obtains real HTTPS certificates after DNS resolves.
7. Port-forward APIs privately for admin commissioning and balance credit.
   Run `commission.py`, monitor readiness/alerts, and perform backup restore
   exercises before opening the public endpoint.

Kubernetes manifests are rendered and structurally validated locally. No
cluster context is configured in this workspace, so admission, PVC provisioning,
NetworkPolicy enforcement, registry pulls and LoadBalancer/HTTPS have not been
run on an actual cluster. Secret/config subPath mounts require pod restart after
rotation; rotate mTLS certificates before their 80-day expiry. Back up PVC data
with the same quiesced database/Redis semantics as Compose; ES is a reconstructible
historical cache, not the authoritative report store. PostgreSQL and write-shadow
contain disposable experiments only.
