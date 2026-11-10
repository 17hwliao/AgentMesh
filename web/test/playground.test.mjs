import test from 'node:test';
import assert from 'node:assert/strict';
import {mountPage} from '../src/product.js';

const reference='agentmesh://images/img_'+'a'.repeat(32);
const event={preventDefault(){}};
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
async function fixture(t,{decode=async()=>({width:64,height:64,close(){}}),chat}={}){
 const saved={document:globalThis.document,createImageBitmap:globalThis.createImageBitmap,create:URL.createObjectURL,revoke:URL.revokeObjectURL};
 const nodes=new Map(),calls=[],created=[],revoked=[];
 const get=s=>{if(!nodes.has(s))nodes.set(s,{value:'',innerHTML:'',textContent:'',classList:{remove(){},add(){}},focus(){}});return nodes.get(s);};
 globalThis.document={querySelector:get,querySelectorAll:()=>[]};globalThis.createImageBitmap=decode;
 URL.createObjectURL=file=>{created.push(file);return 'blob:fixture/'+created.length;};URL.revokeObjectURL=url=>revoked.push(url);
 const view=new AbortController();
 const a={page:'playground',data:{},preview:false,icon:()=>'',isCurrent:v=>v===view&&!view.signal.aborted,error:(target,e)=>target.innerHTML=e.message,backend:async(p,o={})=>{
  calls.push({p,o});if(p==='/v1/models')return {data:{data:[{id:'vision',capabilities:{chat:true,vision:true}},{id:'text',capabilities:{chat:true,vision:false}}]}};
  if(p==='/v1/images')return {data:{reference}};
  if(p==='/v1/chat/completions')return chat?chat(o):new Response('data: {"choices":[{"delta":{"content":"蓝色圆形"}}],"usage":{"prompt_tokens":80,"completion_tokens":4}}\n\ndata: [DONE]\n\n',{headers:{'Content-Type':'text/event-stream'}});
  throw Error('unexpected request '+p);
 }};
 t.after(()=>{view.abort();globalThis.document=saved.document;globalThis.createImageBitmap=saved.createImageBitmap;URL.createObjectURL=saved.create;URL.revokeObjectURL=saved.revoke;});
 await mountPage(a,view);get('#model').value='vision';get('#max-tokens').value='64';get('#prompt').value='描述图片';
 const file=(name='image.png',size=32)=>({name,type:'image/png',size});
 const select=value=>get('#image-file').onchange({target:Object.assign(get('#image-file'),{files:[value]})});
 return {a,get,calls,created,revoked,view,file,select,submit:()=>get('#chat-form').onsubmit(event)};
}
test('upload uses raw image bytes and returned private reference before streaming the answer',async t=>{
 const f=await fixture(t),image=f.file();await f.select(image);await f.submit();
 const upload=f.calls.find(x=>x.p==='/v1/images'),chat=f.calls.find(x=>x.p==='/v1/chat/completions');
 assert.equal(upload.o.body,image);assert.equal(upload.o.headers['Content-Type'],'image/png');
 assert.equal(chat.o.body.messages[0].content[1].image_url.url,reference);assert.equal(chat.o.body.max_tokens,64);
 assert.equal(f.a.data.messages[1].content,'蓝色圆形');assert.equal(f.a.data.messages[1].incomplete,false);assert.equal(f.revoked.length,1);
 // A text-only follow-up must retain the earlier image in the conversation.
 f.get('#prompt').value='右边是什么颜色';await f.submit();
 assert.equal(f.calls.filter(x=>x.p==='/v1/images').length,1);assert.equal(f.calls.filter(x=>x.p==='/v1/chat/completions')[1].o.body.messages[0].content[1].image_url.url,reference);
});
test('out-of-order image decoding keeps the newest selected image and blocks submission while decoding',async t=>{
 const pending=new Map(),closed=[];const f=await fixture(t,{decode:file=>{const d=deferred();pending.set(file.name,d);return d.promise;}});
 const older=f.select(f.file('older.png')),newer=f.select(f.file('newer.png'));await f.submit();assert.equal(f.calls.length,1);assert.equal(f.get('#send-chat').disabled,true);
 pending.get('newer.png').resolve({width:64,height:64,close:()=>closed.push('newer')});await newer;
 pending.get('older.png').resolve({width:64,height:64,close:()=>closed.push('older')});await older;
 assert.match(f.get('#attachment').innerHTML,/newer.png/);assert.equal(f.created.length,1);assert.deepEqual(closed,['newer','older']);await f.submit();assert.equal(f.calls.find(x=>x.p==='/v1/images').o.body.name,'newer.png');
});
test('leaving the page during decoding closes the bitmap without creating a stale preview',async t=>{
 const d=deferred();let closed=false;const f=await fixture(t,{decode:()=>d.promise}),selection=f.select(f.file());f.view.abort();d.resolve({width:64,height:64,close:()=>closed=true});await selection;assert.equal(closed,true);assert.equal(f.created.length,0);
});
test('invalid replacement removes the old attachment instead of sending the wrong image',async t=>{
 const f=await fixture(t);await f.select(f.file('old.png'));await f.select(f.file('too-big.png',256*1024+1));assert.match(f.get('#action-error').innerHTML,/256 KiB/);assert.equal(f.revoked.length,1);await f.submit();assert.equal(f.calls.filter(x=>x.p==='/v1/images').length,0);assert.equal(typeof f.calls.find(x=>x.p==='/v1/chat/completions').o.body.messages[0].content,'string');
});
test('image conversation history is rejected locally when switching to a text-only model',async t=>{
 const f=await fixture(t);f.a.data.messages=[{role:'user',content:[{type:'image_url',image_url:{url:reference}}]}];f.get('#model').value='text';await f.submit();assert.equal(f.calls.length,1);assert.match(f.get('#action-error').innerHTML,/历史包含图片/);
});
test('fourth image is rejected before persisting another asset or calling the model',async t=>{
 const f=await fixture(t);f.a.data.messages=[{role:'user',content:Array.from({length:3},()=>({type:'image_url',image_url:{url:reference}}))}];await f.select(f.file());await f.submit();assert.equal(f.calls.length,1);assert.match(f.get('#action-error').innerHTML,/3 张图片/);
});
test('explicit retry after a rejected chat reuses the confirmed upload and preserves user input',async t=>{
 let count=0;const f=await fixture(t,{chat:async()=>{count++;throw Object.assign(Error('余额不足'),{status:402});}});await f.select(f.file());await f.submit();await f.submit();assert.equal(count,2);assert.equal(f.calls.filter(x=>x.p==='/v1/images').length,1);assert.equal(f.get('#prompt').value,'描述图片');assert.match(f.get('#attachment').innerHTML,/image.png/);
});
test('attachment removal is locked during an in-flight upload/chat operation',async t=>{
 const d=deferred();const f=await fixture(t,{chat:()=>d.promise});await f.select(f.file());const submit=f.submit();await new Promise(setImmediate);assert.equal(f.get('#remove-image').disabled,true);f.get('#remove-image').onclick();assert.match(f.get('#attachment').innerHTML,/image.png/);d.reject(Object.assign(Error('denied'),{status:403}));await submit;assert.equal(f.get('#remove-image').disabled,false);
});
test('new conversation clears pending image decoding and ignores its late result',async t=>{
 const d=deferred();const f=await fixture(t,{decode:()=>d.promise}),selection=f.select(f.file());f.get('#clear-chat').onclick();d.resolve({width:64,height:64,close(){}});await selection;assert.equal(f.created.length,0);assert.equal(f.get('#send-chat').disabled,false);assert.equal(f.get('#attachment').innerHTML,'');
});
