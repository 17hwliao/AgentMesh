// Explicit integration entry: requires an isolated, password-protected Redis.
// Not part of the default dependency-free unit suite.
import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm,readFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {createClient} from 'redis';
import path from 'node:path';
import os from 'node:os';
import {createApp} from '../server.mjs';
import {redisStore,opaque} from '../auth.mjs';
import {provider,bind} from './fixtures/identity.mjs';

test('real Redis preserves encrypted sessions across replicas, isolates products and consumes transactions atomically',async t=>{
 assert.ok(process.env.CONSOLE_TEST_REDIS_URL,'Set CONSOLE_TEST_REDIS_URL for an isolated Redis');assert.ok(process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,'Set a private test password file');
 const idp=await provider(t),dir=await mkdtemp(path.join(os.tmpdir(),'console-redis-')),file=n=>path.join(dir,n);
 await writeFile(file('client'),'fixture-client-secret');await writeFile(file('key'),'3'.repeat(64));await writeFile(file('members'),'{}');
 const c={origin:'http://127.0.0.1:4180',peer_url:'http://127.0.0.1:4181',backend_url:'http://127.0.0.1:1',oidc_issuer:idp.issuer,oidc_client_id:'fixture-client',oidc_client_secret_file:file('client'),session_key_file:file('key'),session_redis_url:process.env.CONSOLE_TEST_REDIS_URL,session_redis_password_file:process.env.CONSOLE_TEST_REDIS_PASSWORD_FILE,members_file:file('members'),email_delivery:'test_sink'};
 const apps=[createApp(c),createApp(c),createApp({...c,origin:c.peer_url,peer_url:c.origin})],urls=await Promise.all(apps.map(bind)),store=redisStore(c);
 t.after(async()=>{for(const app of apps)app.closeAllConnections();await Promise.all(apps.map(app=>new Promise(r=>app.close(r))));await store.close();await rm(dir,{recursive:true,force:true});});
 const login=await idp.login(urls[0]);assert.equal(login.result.status,303);const headers={Cookie:login.cookie,Origin:c.origin};
 const identity=await (await fetch(urls[1]+'/api/auth/session',{headers})).json();assert.equal(identity.subject,'user_a');
 const sid=login.cookie.split('=')[1],stored=await store.get('session',sid);assert.ok(stored);assert.doesNotMatch(stored,/user_a|accessToken|csrf/);
 assert.equal((await fetch(urls[2]+'/api/auth/session',{headers})).status,401);
 const tx=opaque();await store.put('transaction',tx,'controlled-test-value',60);const take=await Promise.all([store.take('transaction',tx),store.take('transaction',tx)]);assert.equal(take.filter(x=>x==='controlled-test-value').length,1);
 const revoked=opaque();await store.put('session',revoked,'controlled-test-value',60);await store.remove('session',revoked);assert.equal(await store.touch('session',revoked,60),0);assert.equal(await store.get('session',revoked),null);
 const rate=opaque();for(let i=0;i<30;i++)assert.equal(await store.rate(rate),true);assert.equal(await store.rate(rate),false);
 // A dropped Redis socket must recover on the next request without restarting
 // the console. Kill only connections in this test's random issuer namespace.
 const admin=createClient({url:c.session_redis_url,password:(await readFile(c.session_redis_password_file,'utf8')).trim()});admin.on('error',()=>{});await admin.connect();
 try{const prefix='console:'+createHash('sha256').update(JSON.stringify([c.oidc_issuer,c.oidc_client_id,c.origin])).digest('hex')+':';const clients=(await admin.sendCommand(['CLIENT','LIST'])).split('\n').map(line=>Object.fromEntries(line.split(' ').map(v=>v.split('='))));const own=clients.filter(v=>v.name?.startsWith(prefix));assert.ok(own.length>=2);for(const v of own)await admin.sendCommand(['CLIENT','KILL','ID',v.id]);await new Promise(r=>setTimeout(r,30));assert.equal(await store.ping(),'PONG');assert.equal((await fetch(urls[1]+'/api/auth/session',{headers})).status,200);}finally{await admin.close();}
 assert.equal((await fetch(urls[0]+'/api/auth/logout',{method:'POST',headers:{...headers,'X-CSRF-Token':identity.csrf_token}})).status,204);assert.equal((await fetch(urls[1]+'/api/auth/session',{headers})).status,401);
});
