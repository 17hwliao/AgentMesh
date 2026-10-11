"""Render single-replica Kubernetes manifests from the release Compose source.

No cluster access or secret values are needed. Runtime secrets are installed
separately with kubectl create secret --from-file. No manifests promise HA.
"""
import argparse,json,subprocess
from pathlib import Path
root=Path(__file__).resolve().parent
p=argparse.ArgumentParser();p.add_argument("--extensions",action="store_true");p.add_argument("--fixture-validation",action="store_true");p.add_argument("--out",type=Path,default=root/"kubernetes/release.json");p.add_argument("--namespace",default="internship-backend");p.add_argument("--am-image",default="local/agentmesh-backend:reviewed");p.add_argument("--ss-image",default="local/sql-sentinel-backend:reviewed");p.add_argument("--proxy-image",default="local/backend-proxy:reviewed");a=p.parse_args()
import re
if not re.fullmatch(r"[a-z][a-z0-9-]{0,62}",a.namespace):p.error("invalid namespace")
files=["-f","compose.yml"]
if a.extensions:files += ["-f","extensions.yml","--profile","extensions"]
result=subprocess.run(["docker","compose",*files,"config","--format","json"],cwd=root,capture_output=True,text=True,encoding="utf-8",timeout=30)
if result.returncode:raise SystemExit("Compose configuration could not be rendered; run setup and configure .env first.")
config=json.loads(result.stdout);namespace=a.namespace
fixture=any("fixtures:" in str(s.get("environment",{})) for s in config["services"].values())
if fixture and not a.fixture_validation:p.error("Fixture endpoints require --fixture-validation; configure real .env before deployment rendering.")
items=[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":namespace,"labels":{"app.kubernetes.io/part-of":"internship-backend"}}}]
def add(kind,name,spec,api="v1"):
    item={"apiVersion":api,"kind":kind,"metadata":{"name":name,"namespace":namespace},"spec":spec};items.append(item);return item
ports={"control":[3306],"baseline":[3306],"candidate":[3306],"redis":[6379],"kafka":[9092,9093],"gateway":[18080],"sentinel-api":[8080],"prometheus":[9090],"otel-collector":[4318,8888],"proxy":[80,443],"model-runner":[50051],"verifier":[50052],"elasticsearch":[9200],"postgres":[5432],"write-shadow":[3306],"jaeger":[4318,16686],"alertmanager":[9093]}
stateful={"control","baseline","candidate","redis","kafka","elasticsearch","postgres","write-shadow","prometheus","proxy","jaeger","alertmanager"}
file_maps={};claim_names=set()
for name,service in config["services"].items():
    profiles=service.get("profiles",[])
    if profiles and not (a.extensions and "extensions" in profiles):continue
    if name in {"seed","pg-compare","write-probe"}:continue
    label={"app.kubernetes.io/name":name}
    container={"name":name,"image":service["image"].replace("local/agentmesh-backend:reviewed",a.am_image).replace("local/sql-sentinel-backend:reviewed",a.ss_image).replace("local/backend-proxy:reviewed",a.proxy_image),"imagePullPolicy":"IfNotPresent"}
    if service.get("entrypoint"):container["command"]=service["entrypoint"]
    if service.get("command"):container["args"]=service["command"]
    env=service.get("environment",{});container["env"]=[{"name":k,"value":str(v)} for k,v in sorted(env.items()) if v is not None]
    memory=int(service.get("mem_limit",256*1024*1024));container["resources"]={"requests":{"memory":"64Mi","cpu":"50m"},"limits":{"memory":str(memory),"cpu":"2"}}
    pod={"automountServiceAccountToken":False,"terminationGracePeriodSeconds":35,"containers":[container],"volumes":[]}
    security={"allowPrivilegeEscalation":False,"capabilities":{"drop":["ALL"]}}
    if service.get("cap_add"):security["capabilities"]["add"]=service["cap_add"]
    if service.get("read_only"):security["readOnlyRootFilesystem"]=True
    if name in {"gateway","usage-relay","usage-worker","am-migrate","model-runner","runner-grants","sentinel-api","sentinel-worker","sentinel-relay","ss-migrate","verifier"}:security.update({"runAsNonRoot":True,"runAsUser":10001,"runAsGroup":10001});pod["securityContext"]={"fsGroup":10001,"seccompProfile":{"type":"RuntimeDefault"}}
    if name=="proxy":security["capabilities"]["add"]=["NET_BIND_SERVICE"]
    if name in {"control","baseline","candidate","write-shadow","postgres"}:
        # Official datastore entrypoints initialize/chown fresh PVCs then
        # drop privileges. Application containers keep ALL capabilities off.
        security["capabilities"]["add"]=["CHOWN","DAC_OVERRIDE","FOWNER","SETGID","SETUID"]
    group={"elasticsearch":1000,"kafka":1000,"prometheus":65534,"alertmanager":65534,"jaeger":10001,"proxy":1000,"postgres":999,"control":999,"baseline":999,"candidate":999,"write-shadow":999,"redis":999}.get(name)
    if group:pod["securityContext"]={"fsGroup":group,"seccompProfile":{"type":"RuntimeDefault"}}
    container["securityContext"]=security
    mounts=[]
    for n,secret in enumerate(service.get("secrets",[])):
        source=secret["source"] if isinstance(secret,dict) else secret
        target=secret.get("target",source) if isinstance(secret,dict) else source
        if not target.startswith("/"):target="/run/secrets/"+target
        v="secret-"+str(n);pod["volumes"].append({"name":v,"secret":{"secretName":"backend-secrets","defaultMode":292,"items":[{"key":source,"path":"value"}]}});mounts.append({"name":v,"mountPath":target,"subPath":"value","readOnly":True})
    for n,volume in enumerate(service.get("volumes",[])):
        v="data-"+str(n);target=volume["target"]
        if volume["type"]=="volume":
            claim=volume["source"].replace("_","-");claim_names.add(claim);pod["volumes"].append({"name":v,"persistentVolumeClaim":{"claimName":claim}});mounts.append({"name":v,"mountPath":target})
        elif volume["type"]=="bind":
            path=Path(volume["source"]).resolve()
            if path.parent!=root or not path.is_file():raise SystemExit("Unsupported host mount in Kubernetes source.")
            key=path.name.replace(".","-").lower();file_maps[key]=path.read_text(encoding="utf-8")
            pod["volumes"].append({"name":v,"configMap":{"name":"backend-config","defaultMode":292,"items":[{"key":key,"path":"value"}]}});mounts.append({"name":v,"mountPath":target,"subPath":"value","readOnly":True})
    if service.get("tmpfs"):
        pod["volumes"].append({"name":"tmp","emptyDir":{"medium":"Memory","sizeLimit":"128Mi"}});mounts.append({"name":"tmp","mountPath":"/tmp"})
    if mounts:container["volumeMounts"]=mounts
    if name in ports:container["ports"]=[{"containerPort":v,"name":"port-"+str(v)} for v in ports[name]]
    hc=service.get("healthcheck",{}).get("test",[])
    if hc:
        probe={"exec":{"command":hc[1:] if hc[0]=="CMD" else ["/bin/sh","-ec",hc[1].replace("$$","$")]},"periodSeconds":10,"timeoutSeconds":5,"failureThreshold":3}
        # Do not use a datastore/outage-sensitive readiness check as liveness:
        # doing so restarts healthy API processes during dependency outages.
        container["readinessProbe"]=probe
        container["startupProbe"]={**probe,"failureThreshold":60}
    if name in {"gateway","sentinel-api"}:
        container["livenessProbe"]={"httpGet":{"path":"/healthz","port":ports[name][0]},"periodSeconds":15,"timeoutSeconds":3,"failureThreshold":3}
    template={"metadata":{"labels":label},"spec":pod}
    if name in {"topics","am-migrate","ss-migrate","jaeger-init","runner-grants"}:
        pod["restartPolicy"]="Never";add("Job",name,{"backoffLimit":3,"activeDeadlineSeconds":300,"ttlSecondsAfterFinished":86400,"template":template},"batch/v1")
    else:
        spec={"replicas":1,"selector":{"matchLabels":label},"template":template}
        if name in stateful:spec["serviceName"]=name;kind="StatefulSet"
        else:kind="Deployment";spec["strategy"]={"type":"Recreate"}
        add(kind,name,spec,"apps/v1")
    if name in ports:
        service_spec={"selector":label,"ports":[{"port":v,"targetPort":v,"name":"port-"+str(v)} for v in ports[name]]}
        if name=="proxy":service_spec["type"]="LoadBalancer"
        add("Service",name,service_spec)
        # Namespace isolation requires a CNI implementing NetworkPolicy.
        # Public traffic reaches only Caddy, whose rules hide admin/metrics.
        peers=[{"podSelector":{}}] if name!="proxy" else [{"ipBlock":{"cidr":"0.0.0.0/0"}}]
        add("NetworkPolicy","allow-"+name,{"podSelector":{"matchLabels":label},"policyTypes":["Ingress"],"ingress":[{"from":peers,"ports":[{"protocol":"TCP","port":v} for v in ports[name]]}]},"networking.k8s.io/v1")
for claim in sorted(claim_names):add("PersistentVolumeClaim",claim,{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"10Gi"}}})
items.append({"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"backend-config","namespace":namespace},"data":file_maps})
add("NetworkPolicy","default-deny-ingress",{"podSelector":{},"policyTypes":["Ingress"]},"networking.k8s.io/v1")
for item in items:item["metadata"].setdefault("annotations",{})["backend.agentmesh.local/validation-scope"]="fixture-only-do-not-deploy" if a.fixture_validation else "configured-not-cluster-validated"
a.out.parent.mkdir(parents=True,exist_ok=True);a.out.write_text(json.dumps({"apiVersion":"v1","kind":"List","items":items},ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
print(f"Rendered {len(items)} resources without credentials or cluster access: {a.out.name}")
