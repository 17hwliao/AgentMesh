"""Prepare private Casdoor configuration. No network, mail or model calls."""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
from urllib.parse import urlsplit

CASDOOR_VERSION = 'v4.23.0'

def origin(value, local=False):
    if re.search(r'[\x00-\x20\x7f]',value):raise ValueError('origin contains whitespace or controls')
    u=urlsplit(value)
    if u.username or u.password or u.path not in ('','/') or u.query or u.fragment or not u.hostname:
        raise ValueError('an origin without paths or credentials is required')
    if u.scheme!='https' and not (local and u.scheme=='http' and u.hostname in ('localhost','127.0.0.1','::1')):
        raise ValueError('HTTPS required except explicit loopback development')
    if u.hostname!='::1' and not re.fullmatch(r'[A-Za-z0-9.-]+',u.hostname):raise ValueError('invalid hostname')
    _=u.port
    return u.scheme+'://'+u.netloc

def private_write(path, value):
    with os.fdopen(os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600),'w',encoding='utf-8',newline='\n') as f:f.write(value)
    if os.name!='nt' and os.geteuid()==0:os.chown(path,1000,1000)

def configure(args):
    root=args.deploy_dir.resolve();base=root/'identity-private'
    issuer=origin(args.issuer,args.local);mesh=origin(args.mesh_origin,args.local);sql=origin(args.sentinel_origin,args.local)
    if len({issuer,mesh,sql})!=3:raise ValueError('identity and products require distinct origins')
    if args.test_mail and not args.local:raise ValueError('test inbox is local-only')
    if bool(args.smtp_from)!=bool(args.smtp_secret_file):raise ValueError('SMTP address and private authorization file are required together')
    if args.test_mail and args.smtp_from:raise ValueError('choose test inbox or real SMTP')
    if args.smtp_from and not re.fullmatch(r'[A-Za-z0-9._+-]+@qq\.com',args.smtp_from,re.I):raise ValueError('this preset requires a QQ sender')
    authorization=''
    if args.smtp_secret_file:
        authorization=args.smtp_secret_file.read_text(encoding='utf-8-sig').strip()
        if not re.fullmatch(r'[A-Za-z]{16}',authorization):raise ValueError('QQ SMTP authorization code must be 16 letters, not a QQ password')
    if base.exists():raise ValueError('identity-private exists; do not overwrite live users, clients or secrets')
    if os.name!='nt' and os.geteuid() not in (0,1000):raise ValueError('run as root or UID 1000')
    delivery='test_sink' if args.test_mail else 'smtp' if authorization else 'unconfigured'
    admin=secrets.token_urlsafe(36);db=secrets.token_hex(32);root_db=secrets.token_hex(32);redis=secrets.token_hex(32)
    clients={k:dict(id=secrets.token_hex(16),secret=secrets.token_hex(32)) for k in ('mesh','sentinel')}
    provider_list=[]
    if delivery!='unconfigured':
        provider_list.append(dict(owner='admin',name='console-email',displayName='Console email',category='Email',type='Default',host='mailpit' if args.test_mail else 'smtp.qq.com',port=1025 if args.test_mail else 465,sslMode='Disable' if args.test_mail else 'Enable',clientId='' if args.test_mail else args.smtp_from,clientSecret=authorization,clientId2='console@example.test' if args.test_mail else args.smtp_from,title='登录验证码',content='你的登录验证码为 %s，有效期 5 分钟。如非本人操作，请忽略。'))
    if not args.local:provider_list.append(dict(owner='admin',name='console-captcha',displayName='Verification',category='Captcha',type='Default'))
    applications=[]
    for name,product_origin in [('mesh',mesh),('sentinel',sql)]:
        providers=[dict(name=p['name'],canSignUp=True,canSignIn=True,canUnlink=False,rule='Always' if p['category']=='Captcha' else 'All') for p in provider_list]
        applications.append(dict(owner='admin',name=name+'-console',displayName='AgentMesh' if name=='mesh' else 'SQL Sentinel',organization='internship',homepageUrl=product_origin,cert='cert-built-in',enablePassword=False,enableSignUp=True,disableSignin=False,enableSigninSession=True,enableAutoSignin=True,clientId=clients[name]['id'],clientSecret=clients[name]['secret'],redirectUris=[product_origin+'/auth/callback'],grantTypes=['authorization_code'],tokenFormat='JWT-Standard',tokenSigningMethod='RS256',tokenFields=[],expireInHours=8,refreshExpireInHours=0,cookieExpireInHours=8,failedSigninLimit=5,failedSigninFrozenTime=15,providers=providers,signinMethods=[dict(name='Verification code',displayName='邮箱验证码',rule='Email only')],signupItems=[dict(name='ID',visible=False,required=True,rule='Random'),dict(name='Username',visible=True,required=True,rule='None'),dict(name='Display name',visible=False,required=False,rule='None'),dict(name='Email',visible=True,required=True,rule='Normal'),dict(name='Signup button',visible=True,required=False,rule='None')],signinItems=[dict(name='Username',visible=True,rule='Email'),dict(name='Verification code',visible=True,rule='None'),dict(name='Captcha',visible=not args.local,rule='popup'),dict(name='Login button',visible=True,rule='None'),dict(name='Signup link',visible=True,rule='None')],themeData=dict(themeType='light',colorPrimary='#181818',borderRadius=6)))
    seed=dict(organizations=[dict(owner='admin',name='internship',displayName='共享身份中心',passwordType='bcrypt',passwordOptions=['AtLeast8'],defaultApplication='mesh-console',defaultTokenFormat='JWT-Standard',languages=['zh','en'],countryCodes=['CN'],isProfilePublic=False,enableSoftDeletion=True,accountItems=[dict(name='ID',visible=True,viewRule='Self',modifyRule='Immutable'),dict(name='Email',visible=True,viewRule='Self',modifyRule='Admin'),dict(name='Display name',visible=True,viewRule='Self',modifyRule='Self')])],applications=applications,providers=provider_list)
    config='\n'.join(['appname = casdoor','httpport = 8000','runmode = prod','copyrequestbody = true','driverName = mysql','dataSourceName = casdoor:'+db+'@tcp(identity-db:3306)/','dbName = casdoor','showSql = false','origin = '+issuer,'originFrontend = '+issuer,'staticBaseUrl = '+issuer,'frontendBaseDir = ./web/build','isDemoMode = false','showGithubCorner = false','forceLanguage = zh','defaultLanguage = zh','defaultApplication = mesh-console','verificationCodeTimeout = 5','initAdminPassword = '+admin,'initDataFile = /conf/init_data.json','initDataNewOnly = true','logPostOnly = true','logConfig = {"adapter":"console"}'])+'\n'
    metadata=dict(version=CASDOOR_VERSION,issuer=issuer,mesh_origin=mesh,sentinel_origin=sql,email_delivery=delivery,local=args.local,clients={k:dict(id=v['id'],secret_file=k+'_client_secret') for k,v in clients.items()})
    root.mkdir(parents=True,exist_ok=True);base.mkdir(mode=0o700)
    if os.name!='nt' and os.geteuid()==0:os.chown(base,1000,1000)
    files={'app.conf':config,'init_data.json':json.dumps(seed,ensure_ascii=False,indent=2)+'\n','identity.json':json.dumps(metadata,indent=2)+'\n','admin_password':admin,'mysql_root':root_db,'mysql_password':db,'redis_password':redis}
    files.update({k+'_client_secret':v['secret'] for k,v in clients.items()})
    for name,value in files.items():private_write(base/name,value)
    return dict(status='prepared',identity='casdoor',version=CASDOOR_VERSION,email_delivery=delivery,live_email_verified=False,network_calls=0)

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--deploy-dir',type=Path,default=Path.cwd());p.add_argument('--issuer',required=True);p.add_argument('--mesh-origin',required=True);p.add_argument('--sentinel-origin',required=True);p.add_argument('--local',action='store_true');p.add_argument('--test-mail',action='store_true');p.add_argument('--smtp-from');p.add_argument('--smtp-secret-file',type=Path)
    try:print(json.dumps(configure(p.parse_args())))
    except (OSError,ValueError):raise SystemExit('Identity setup refused. Check origins, private file access and existing output. No secrets printed.')
