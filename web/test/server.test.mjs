import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {provider,TestStore,bind} from './fixtures/identity.mjs';
import {createApp,validateConfig} from '../server.mjs';
import product from '../product.json' with {type:'json'};

const mesh=product.id==='agentmesh';
async function fixture(t){
 const dir=await mkdtemp(path.join(os.tmpdir(),'console-test-')),calls=[],idp=await provider(t);const file=n=>path.join(dir,n);
 const f={dir,calls,sessionActive:true,verified:true,banned:false,members:{user_a:{enabled:true,tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders'],gateway_key_file:file('gateway'),can_manage_keys:true}}};
 f.identity=idp;for(const [field,target]of [['sessionActive','active'],['verified','verified'],['banned','banned']])Object.defineProperty(f,field,{get:()=>idp[target],set:value=>idp[target]=value});
 await writeFile(file('client'),'fixture-client-secret');await writeFile(file('session'),'1'.repeat(64));await writeFile(file('redis'),'redis-fixture');await writeFile(file('admin'),'internal-admin-fixture');await writeFile(file('gateway'),'am_fixture_tenant_a');
 const image=Buffer.from([137,80,78,71,13,10,26,10,0,255,128,1]);
 const backend=http.createServer(async(req,res)=>{const chunks=[];for await(const x of req)chunks.push(x);const raw=Buffer.concat(chunks);calls.push({method:req.method,path:req.url,auth:req.headers.authorization,body:raw.length&&req.headers['content-type']?.startsWith('application/json')?JSON.parse(raw.toString()):null,raw,contentType:req.headers['content-type']});res.setHeader('Content-Type','application/json');
  if(req.url==='/v1/account')return res.end(JSON.stringify({tenant_id:f.wrongTenant?'tenant_b':'tenant_a',enabled:true}));
  if(req.url==='/auth/tokens')return res.end(JSON.stringify({token:'scoped-backend-token',expires_in:3600}));
  if(req.url.startsWith('/admin/tenants/'))return res.end(JSON.stringify({data:[{key_id:'owned_key',enabled:true}],next_after:''}));
  if(req.url==='/admin/api-keys')return res.end(JSON.stringify({api_key:'am_new_fixture',key_id:'owned_new'}));
  if(req.url==='/v1/images'&&req.method==='POST')return res.writeHead(201).end(JSON.stringify({id:'img_'+'a'.repeat(32),reference:'agentmesh://images/img_'+'a'.repeat(32)}));
  if(req.url==='/v1/images/img_'+'a'.repeat(32)&&req.method==='GET'){res.setHeader('Content-Type','image/png');return res.end(image);}
  if(req.method==='DELETE'){res.statusCode=204;return res.end();}
  if(req.url==='/v1/chat/completions'){res.setHeader('Content-Type','text/event-stream');res.setHeader('X-AgentMesh-Billing-Request-ID','req_fixture');return res.end('data: {"choices":[{"delta":{"content":"hello"}}]}\n\ndata: [DONE]\n\n');}
  if(req.url.endsWith('/report'))res.setHeader('X-Sentinel-Report-SHA256','a'.repeat(64));
  res.end(JSON.stringify({data:[],ok:true}));
 });
 const backendURL=await bind(backend);
 const config={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:backendURL,oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('session'),session_redis_url:'redis://127.0.0.1:6379/0',session_redis_password_file:file('redis'),email_delivery:'test_sink',backend_admin_file:file('admin'),members_file:file('members')};
 f.members.user_a.issuer=idp.issuer;
 const save=()=>writeFile(file('members'),JSON.stringify(f.members));await save();f.save=save;
 f.store=new TestStore();const app=createApp(config,{sessionStore:f.store});f.url=await bind(app);f.config=config;f.image=image;
 t.after(async()=>{app.closeAllConnections();backend.closeAllConnections();await Promise.all([new Promise(r=>app.close(r)),new Promise(r=>backend.close(r))]);await rm(dir,{recursive:true,force:true});});
 const logged=await idp.login(f.url);assert.equal(logged.result.status,303);f.cookie=logged.cookie;
 f.csrf=(await (await fetch(f.url+'/api/auth/session',{headers:{Cookie:f.cookie}})).json()).csrf_token;
 f.request=async(p,opts={})=>fetch(f.url+p,{...opts,headers:{Cookie:f.cookie,Origin:config.origin,'X-CSRF-Token':f.csrf,...opts.headers}});
 return f;
}
test('unconfigured console fails closed while static config remains public',async t=>{const app=createApp({origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181'});const url=await bind(app);t.after(()=>{app.closeAllConnections();app.close();});assert.equal((await fetch(url+'/api/session')).status,503);const data=await (await fetch(url+'/api/config')).json();assert.equal(data.auth_configured,false);assert.equal('clerk_secret_file'in data,false);});
test('same issuer identity is mapped to the current product membership',async t=>{const f=await fixture(t);const r=await f.request('/api/session');assert.equal(r.status,200);const data=await r.json();assert.equal(data.subject,'user_a');assert.equal(data.tenant_id,'tenant_a');assert.equal('gateway_key_file'in data,false);assert.equal(f.calls.length,0);});
test('browser Bearer tokens and forged or duplicate cookies cannot replace a server session',async t=>{const f=await fixture(t);for(const headers of [{Authorization:'Bearer unsigned.invalid.token'},{Cookie:f.cookie.replace(/=.*/, '='+'a'.repeat(43))},{Cookie:f.cookie+'; '+f.cookie}])assert.equal((await fetch(f.url+'/api/session',{headers})).status,401);assert.equal(f.calls.length,0);});
test('unverified email and revoked identity tokens cannot use business APIs',async t=>{const f=await fixture(t);f.verified=false;assert.equal((await f.request('/api/session')).status,403);f.verified=true;f.sessionActive=false;assert.equal((await f.request('/api/backend/'+(mesh?'v1/models':'jobs/job1'))).status,401);assert.equal(f.calls.length,0);});
test('membership removal and disable take effect without restarting',async t=>{const f=await fixture(t);assert.equal((await f.request('/api/session')).status,200);f.members.user_a.enabled=false;await f.save();assert.equal((await f.request('/api/session')).status,403);delete f.members.user_a;await f.save();assert.equal((await f.request('/api/session')).status,403);});
test('foreign origins and arbitrary admin routes are denied',async t=>{const f=await fixture(t);assert.equal((await f.request('/api/session',{headers:{Origin:'https://evil.invalid'}})).status,403);for(const p of ['/api/backend/admin/tenants','/api/backend/auth/tokens','/api/backend/metrics','/api/backend/jobs/job1?tenant_id=other'])assert.equal((await f.request(p)).status,404);assert.equal(f.calls.length,0);});
test('upstream receives only scoped server credential, never browser token',async t=>{const f=await fixture(t);const r=await f.request('/api/backend/'+(mesh?'v1/models':'jobs/job1'));assert.equal(r.status,200);const last=f.calls.at(-1);assert.equal(last.auth,mesh?'Bearer am_fixture_tenant_a':'Bearer scoped-backend-token');if(!mesh){const issue=f.calls.find(x=>x.path==='/auth/tokens');assert.deepEqual(issue.body,{subject:'user_a',tenant_id:'tenant_a',project_id:'project_a',role:'writer',tables:['orders']});}});
test('response association headers survive console proxy',async t=>{const f=await fixture(t);const r=await f.request(mesh?'/api/backend/v1/chat/completions':'/api/backend/jobs/job1/report',mesh?{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'}:{});assert.equal(r.status,200);assert.equal(r.headers.get(mesh?'x-agentmesh-billing-request-id':'x-sentinel-report-sha256'),mesh?'req_fixture':'a'.repeat(64));if(mesh)assert.match(await r.text(),/data: \[DONE\]/);});
test('private image upload and download retain binary bytes through the authenticated console proxy',async t=>{
 if(!mesh)return;const f=await fixture(t),r=await f.request('/api/backend/v1/images',{method:'POST',headers:{'Content-Type':'image/png'},body:f.image});assert.equal(r.status,201);assert.equal((await r.json()).reference,'agentmesh://images/img_'+'a'.repeat(32));
 const upload=f.calls.find(x=>x.path==='/v1/images');assert.deepEqual(upload.raw,f.image);assert.equal(upload.contentType,'image/png');assert.equal(upload.auth,'Bearer am_fixture_tenant_a');
 const image=await f.request('/api/backend/v1/images/img_'+'a'.repeat(32));assert.equal(image.headers.get('content-type'),'image/png');assert.equal(image.headers.get('cache-control'),'no-store');assert.deepEqual(Buffer.from(await image.arrayBuffer()),f.image);
});
test(mesh?'Key creation ignores no browser-supplied tenant and rejects foreign Key revocation':'role changes invalidate cached scoped SQL token',async t=>{const f=await fixture(t);if(mesh){assert.equal((await f.request('/api/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:'{"tenant_id":"tenant_b"}'})).status,400);assert.equal((await f.request('/api/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,200);assert.deepEqual(f.calls.find(x=>x.path==='/admin/api-keys').body,{tenant_id:'tenant_a'});assert.equal((await f.request('/api/keys/other_key',{method:'DELETE'})).status,404);assert.equal(f.calls.some(x=>x.path==='/admin/api-keys/other_key'),false);assert.equal((await f.request('/api/keys/owned_key',{method:'DELETE'})).status,204);}else{await f.request('/api/backend/jobs/job1');f.members.user_a.role='reader';await f.save();await f.request('/api/backend/jobs/job1');assert.equal(f.calls.filter(x=>x.path==='/auth/tokens').length,2);assert.equal(f.calls.findLast(x=>x.path==='/auth/tokens').body.role,'reader');}});
test(mesh?'misbound gateway credential cannot read another tenant':'malformed role grants no SQL access',async t=>{const f=await fixture(t);if(mesh){f.wrongTenant=true;assert.equal((await f.request('/api/backend/v1/models')).status,403);assert.equal(f.calls.some(x=>x.path==='/v1/models'),false);}else{f.members.user_a.role='superadmin';await f.save();assert.equal((await f.request('/api/session')).status,503);assert.equal(f.calls.length,0);}});
test('configuration refuses legacy identity settings and overlapping console origins',()=>{assert.throws(()=>validateConfig({origin:'https://a.example.com',peer_url:'https://a.example.com'}));assert.throws(()=>validateConfig({origin:'https://a.example.com',peer_url:'https://b.example.com',clerk_issuer:'https://old.invalid'}));});


test('list queries retain scope and reject caller-supplied tenant or unknown parameters',async t=>{
 const f=await fixture(t),base=mesh?'/v1/images':'/jobs';
 assert.equal((await f.request('/api/backend'+base+'?after=cursor1'+(mesh?'':'&limit=25&status=COMPLETED'))).status,200);
 assert.match(f.calls.at(-1).path,/after=cursor1/);
 const before=f.calls.length;
 for(const query of ['tenant_id=other','after=a&after=b','unknown=x'])assert.equal((await f.request('/api/backend'+base+'?'+query)).status,400);
 assert.equal(f.calls.length,before);
});
test('platform admin routes require an explicit server-side grant',async t=>{
 const f=await fixture(t);assert.equal((await f.request('/api/admin/tenants')).status,403);
 f.members.user_a.platform_admin=true;await f.save();
 const response=await f.request('/api/admin/tenants?limit=50');assert.equal(response.status,mesh?200:403);
 if(mesh){assert.equal(f.calls.at(-1).auth,'Bearer internal-admin-fixture');assert.equal((await f.request('/api/admin/billing/credits',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({tenant_id:'tenant_a',credit_id:'stable_credit',proof:'operator_receipt',amount_nano:1000000000})})).status,200);assert.equal(f.calls.at(-1).body.credit_id,'stable_credit');assert.equal((await f.request('/api/admin/auth/tokens')).status,404);}
});
test('scoped SQL session management is admin-only and never exposes bootstrap issuance',async t=>{
 if(mesh)return;const f=await fixture(t);assert.equal((await f.request('/api/backend/auth/sessions')).status,404);
 f.members.user_a.role='admin';await f.save();assert.equal((await f.request('/api/backend/auth/sessions?after=abc')).status,200);assert.equal(f.calls.at(-1).auth,'Bearer scoped-backend-token');assert.equal((await f.request('/api/backend/auth/tokens',{method:'POST',body:'{}'})).status,404);
});
test('production rejects development credentials and placeholder public API URLs',()=>{
 assert.throws(()=>validateConfig({origin:'https://a.example.net',peer_url:'https://b.example.net',production:true}));
 assert.throws(()=>validateConfig({origin:'https://a.example.net',peer_url:'https://b.example.net',public_api_url:'https://api.example.com/v1'}));
 assert.throws(()=>validateConfig({origin:'https://a.example.net',peer_url:'https://b.example.net',public_api_url:'https://api.example.net/v1?token=secret'}));
});

test('Key management requires literal true rather than a truthy misconfigured flag',async t=>{const f=await fixture(t);f.members.user_a.can_manage_keys='false';await f.save();const before=f.calls.filter(x=>x.path==='/admin/api-keys').length;assert.equal((await f.request('/api/keys',{method:'POST',body:'{}'})).status,403);assert.equal(f.calls.filter(x=>x.path==='/admin/api-keys').length,before);});
