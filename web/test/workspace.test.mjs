import test from 'node:test';
import assert from 'node:assert/strict';
import {mountWorkspace} from '../src/workspace.js';

test('uncertain administrative writes keep the stable credit ID and block duplicate submission',async()=>{
 const nodes=new Map(),get=s=>{if(!nodes.has(s))nodes.set(s,{value:'',innerHTML:'',checked:true,setAttribute(){},removeAttribute(){}});return nodes.get(s);};globalThis.document={querySelector:get,querySelectorAll:()=>[]};const writes=[];let release;
 try{
 await mountWorkspace({page:'administration',preview:false,session:{platform_admin:true},isCurrent:()=>true,error:(target,e)=>target.innerHTML=e.message,request:async(p,o)=>{if(o?.method==='POST'){writes.push(o.body);await new Promise(r=>release=r);throw Object.assign(Error('unavailable'),{status:503});}return {data:{data:[],next_after:''}};}},new AbortController());
 get('#admin-operation').value='credit';get('#admin-payload').value=JSON.stringify({tenant_id:'tenant_a',credit_id:'stable_credit',proof:'test_proof',amount_nano:1000000000});get('#admin-confirm').checked=true;
 const submit=()=>get('#admin-form').onsubmit({preventDefault(){}}),first=submit();await submit();assert.equal(writes.length,1);release();await first;await submit();assert.equal(writes.length,1);assert.equal(writes[0].credit_id,'stable_credit');assert.match(get('#admin-result').innerHTML,/结果尚未确认/);assert.equal(get('#admin-submit').disabled,true);
 }finally{delete globalThis.document;}
});
