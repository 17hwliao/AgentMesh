"""Read-only onboarding checks and isolated OpenCode/Claude Code profiles.

No model request, recharge, global configuration overwrite or embedded API key.
"""
import argparse,json,os
from datetime import datetime,timezone
from pathlib import Path
import urllib.request,urllib.parse,urllib.error
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs):return None
def endpoint(raw):
    u=urllib.parse.urlsplit(raw)
    if u.scheme not in {'http','https'} or not u.hostname or u.username or u.password or u.query or u.fragment or any(c.isspace() for c in raw):raise ValueError('explicit HTTP(S) base URL required without credentials/query/fragment')
    if u.scheme=='http' and u.hostname not in {'127.0.0.1','localhost','::1'}:raise ValueError('remote gateway requires HTTPS')
    base=raw.rstrip('/');return base[:-3] if base.endswith('/v1') else base
def generate(base,model,key_file,out):
    root=endpoint(base);key=key_file.read_text(encoding='utf-8-sig').strip()
    if not key or len(key)>16384 or any(not 33<=ord(c)<=126 for c in key):raise ValueError('invalid private gateway key file')
    opener=urllib.request.build_opener(NoRedirect())
    def get(path):
        r=urllib.request.Request(root+path,headers={'Authorization':'Bearer '+key})
        try:
            with opener.open(r,timeout=10) as p:
                raw=p.read((1<<20)+1)
                if len(raw)>1<<20:raise ValueError('response too large')
                return json.loads(raw)
        except urllib.error.HTTPError as e:raise ValueError('gateway check failed (HTTP '+str(e.code)+'); verify the gateway key, routes, wallet and readiness') from None
    models=get('/v1/models');row=next((x for x in models['data'] if x['id']==model),None)
    if row is None or not row.get('capabilities',{}).get('tools'):raise ValueError('model not visible or tool calling is not enabled for this key')
    wallet=get('/v1/billing/balance');prices=get('/v1/billing/prices')
    catalog=prices.get('catalog',{})
    if wallet.get('currency')!='CNY' or wallet.get('scale')!=1000000000 or catalog.get('currency')!='CNY':raise ValueError('expected a CNY wallet and tariff')
    if datetime.fromisoformat(catalog['expires_at'].replace('Z','+00:00'))<=datetime.now(timezone.utc):raise ValueError('price catalog expired; administrator must refresh verified rates')
    if wallet.get('available_nano',0)<=0:raise ValueError('wallet has no available balance; administrator must provision it')
    if not any(x['model']==model for x in prices.get('catalog',{}).get('prices',[])):raise ValueError('selected model has no current tariff')
    reserve=3*max((x['max_input_tokens']*x['input_nano_per_million']+4096*x['output_nano_per_million']+999999)//1000000 for x in prices['catalog']['prices'] if x['model']==model)
    if wallet['available_nano']<reserve:raise ValueError('wallet cannot cover the configured maximum-attempt hold; ask the administrator to provision balance')
    if get('/readyz').get('status')!='ready':raise ValueError('gateway is not ready')
    if out.exists():raise ValueError('output directory exists; use a new private directory')
    out.mkdir(parents=True,mode=0o700)
    vision=row['capabilities'].get('vision',False)
    config={'$schema':'https://opencode.ai/config.json','model':'agentmesh/'+model,'small_model':'agentmesh/'+model,'enabled_providers':['agentmesh'],'provider':{'agentmesh':{'npm':'@ai-sdk/openai-compatible','name':'AgentMesh','options':{'baseURL':root+'/v1','apiKey':'{env:AGENTMESH_API_KEY}'},'models':{model:{'name':'AgentMesh '+model,'limit':{'context':8192,'output':4096},'modalities':{'input':['text','image'] if vision else ['text'],'output':['text']}}}}}}
    env={'ANTHROPIC_BASE_URL':root,'ANTHROPIC_MODEL':model,'ANTHROPIC_DEFAULT_HAIKU_MODEL':model,'ANTHROPIC_DEFAULT_SONNET_MODEL':model,'ANTHROPIC_DEFAULT_OPUS_MODEL':model,'CLAUDE_CODE_MAX_OUTPUT_TOKENS':'4096','MAX_THINKING_TOKENS':'0','CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1'}
    (out/'opencode.json').write_text(json.dumps(config,indent=2)+'\n',encoding='utf-8')
    (out/'claude-settings.json').write_text(json.dumps({'env':env},indent=2)+'\n',encoding='utf-8')
    # A simple script deliberately avoids PowerShell common parameters: -p is
    # Claude's print flag and must not bind to ProgressAction/PipelineVariable.
    head='''param([string]$KeyFile)
$ClientArgs=$args
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($KeyFile)) { throw 'Gateway -KeyFile is required' }
$gatewayKey = [System.IO.File]::ReadAllText((Resolve-Path -LiteralPath $KeyFile).Path).Trim()
if ([string]::IsNullOrWhiteSpace($gatewayKey)) { throw 'Empty gateway key file' }
'''
    (out/'run-opencode.ps1').write_text(head+'''$previousKey=$env:AGENTMESH_API_KEY
$previousConfig=$env:OPENCODE_CONFIG
try {
  $env:AGENTMESH_API_KEY=$gatewayKey
  $env:OPENCODE_CONFIG=Join-Path $PSScriptRoot 'opencode.json'
  & opencode @ClientArgs
  $clientExit=$LASTEXITCODE
} finally { $env:AGENTMESH_API_KEY=$previousKey; $env:OPENCODE_CONFIG=$previousConfig }
exit $clientExit
''',encoding='utf-8')
    (out/'run-claude.ps1').write_text(head+'''$previousKey=$env:ANTHROPIC_API_KEY
$previousToken=$env:ANTHROPIC_AUTH_TOKEN
try {
  $env:ANTHROPIC_API_KEY=$gatewayKey
  Remove-Item Env:ANTHROPIC_AUTH_TOKEN -ErrorAction SilentlyContinue
  & claude --settings (Join-Path $PSScriptRoot 'claude-settings.json') @ClientArgs
  $clientExit=$LASTEXITCODE
} finally { $env:ANTHROPIC_API_KEY=$previousKey; $env:ANTHROPIC_AUTH_TOKEN=$previousToken }
exit $clientExit
''',encoding='utf-8')
    for p in out.iterdir():
        assert key not in p.read_text(encoding='utf-8'),'credential embedded in generated profile'
        if os.name!='nt':p.chmod(0o600)
    return {'status':'PASS','model':model,'tools':True,'vision':vision,'paid_calls':0,'key_embedded':False,'files':4,'output':str(out)}
if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--base-url',required=True);p.add_argument('--model',default='domestic-chat');p.add_argument('--key-file',required=True,type=Path);p.add_argument('--out',required=True,type=Path);a=p.parse_args()
    try:print(json.dumps(generate(a.base_url,a.model,a.key_file,a.out),ensure_ascii=False))
    except (ValueError,OSError,KeyError) as e:p.exit(1,str(e)+'\n')
