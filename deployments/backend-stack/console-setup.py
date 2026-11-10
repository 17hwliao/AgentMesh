"""Generate two independent Casdoor OIDC console configurations; preserve private state."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
from urllib.parse import urlsplit

spec=importlib.util.spec_from_file_location('identity_setup',Path(__file__).with_name('identity-setup.py'))
identity=importlib.util.module_from_spec(spec);spec.loader.exec_module(identity)

def read_secret(path):
    value=path.read_text(encoding='utf-8-sig').strip()
    if not value or len(value)>16384 or '\n' in value or '\r' in value:raise ValueError('private credential is empty or malformed')
    return value

def configure(args):
    root=args.deploy_dir.resolve();directory=(args.identity_dir or root/'identity-private').resolve()
    if not (root/'compose.yml').is_file() or not (root/'.env').is_file():raise ValueError('run backend setup first')
    if os.name!='nt' and os.geteuid() not in (0,1000):raise ValueError('run as root or UID 1000')
    metadata=json.loads((directory/'identity.json').read_text(encoding='utf-8'))
    local=metadata['local']
    if not isinstance(local,bool) or metadata['email_delivery'] not in ('unconfigured','smtp','test_sink') or (not local and metadata['email_delivery']=='test_sink'):raise ValueError('invalid deployment mode')
    issuer=identity.origin(metadata['issuer'],local)
    mesh=identity.origin(metadata['mesh_origin'],local);sql=identity.origin(metadata['sentinel_origin'],local)
    public=identity.origin(args.api_origin,local)
    if len({issuer,mesh,sql,public})!=4:raise ValueError('use distinct identity, API and product origins')
    if not local and urlsplit(mesh).hostname.split('.',1)[-1]!=urlsplit(sql).hostname.split('.',1)[-1]:raise ValueError('product subdomains must share their parent domain')
    for name in ('mesh','sentinel'):
        if not re.fullmatch(r'[A-Za-z0-9_-]{1,128}',metadata['clients'][name]['id']):raise ValueError('invalid client ID')
    for value in (args.user_id,args.tenant_id,args.project_id,*args.tables):
        if not re.fullmatch(r'[A-Za-z0-9_-]{1,64}',value):raise ValueError('use stable bounded IDs and explicit table names')
    if not 1<=len(args.tables)<=16:raise ValueError('provide 1..16 allowed tables')
    credentials={k:read_secret(v) for k,v in dict(gateway=args.gateway_key_file,mesh_admin=args.gateway_admin_file,sentinel_admin=args.sentinel_bootstrap_file,redis=directory/'redis_password',mesh_client=directory/'mesh_client_secret',sentinel_client=directory/'sentinel_client_secret').items()}
    base=root/'console-private'
    if base.exists():raise ValueError('console-private exists; preserve existing member grants and credentials')
    configs={}
    for product,origin,peer,backend in [('mesh',mesh,sql,'http://gateway:18080'),('sentinel',sql,mesh,'http://sentinel-api:8080')]:
        configs[product]=dict(production=not local,origin=origin,peer_url=peer,oidc_issuer=issuer,oidc_client_id=metadata['clients'][product]['id'],oidc_client_secret_file='/run/console-private/oidc_client_secret',session_key_file='/run/console-private/session_key',session_redis_url='redis://console-sessions:6379/0',session_redis_password_file='/run/console-private/redis_password',email_delivery=metadata['email_delivery'],members_file='/run/console-private/members.json',backend_admin_file='/run/console-private/admin_token',backend_url=backend)
        if product=='mesh':configs[product]['public_api_url']=public+'/v1'
    members={'mesh':{args.user_id:dict(issuer=issuer,enabled=True,tenant_id=args.tenant_id,gateway_key_file='/run/console-private/gateway_key',can_manage_keys=args.platform_admin,platform_admin=args.platform_admin)},'sentinel':{args.user_id:dict(issuer=issuer,enabled=True,tenant_id=args.tenant_id,project_id=args.project_id,role=args.sql_role,tables=args.tables)}}
    base.mkdir(mode=0o700)
    if os.name!='nt' and os.geteuid()==0:os.chown(base,1000,1000)
    for product in configs:
        folder=base/product;folder.mkdir(mode=0o700)
        if os.name!='nt' and os.geteuid()==0:os.chown(folder,1000,1000)
        files={'config.json':json.dumps(configs[product],indent=2)+'\n','members.json':json.dumps(members[product],indent=2)+'\n','oidc_client_secret':credentials[product+'_client'],'session_key':secrets.token_hex(32),'redis_password':credentials['redis'],'admin_token':credentials[product+'_admin']}
        if product=='mesh':files['gateway_key']=credentials['gateway']
        for name,value in files.items():identity.private_write(folder/name,value)
    env=root/'.env';text=env.read_text(encoding='utf-8')
    # Local initialization does not overwrite production host interpolation values.
    if not local:
        for key,value in dict(APP_DOMAIN=urlsplit(public).netloc,MESH_CONSOLE_DOMAIN=urlsplit(mesh).netloc,SENTINEL_CONSOLE_DOMAIN=urlsplit(sql).netloc,AUTH_DOMAIN=urlsplit(issuer).netloc).items():
            pattern=re.compile('^'+key+'=.*$',re.M);text=pattern.sub(key+'='+value,text) if pattern.search(text) else text.rstrip()+'\n'+key+'='+value+'\n'
        temporary=root/'.env.console-tmp';identity.private_write(temporary,text);os.replace(temporary,env)
    return dict(status='configured',identity='casdoor',email_delivery=metadata['email_delivery'],live_login_verified=False,cloud_deployed=False)

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--deploy-dir',type=Path,default=Path.cwd());p.add_argument('--identity-dir',type=Path);p.add_argument('--api-origin',required=True)
    for name in ('user-id','tenant-id','project-id'):p.add_argument('--'+name,required=True)
    for name in ('gateway-key-file','gateway-admin-file','sentinel-bootstrap-file'):p.add_argument('--'+name,type=Path,required=True)
    p.add_argument('--tables',nargs='+',required=True);p.add_argument('--sql-role',choices=['reader','writer','reviewer','admin'],default='writer');p.add_argument('--platform-admin',action='store_true')
    try:print(json.dumps(configure(p.parse_args())))
    except (OSError,ValueError,KeyError):raise SystemExit('Console setup refused. Check identity metadata, grants, private file access and existing output. No credentials printed.')
