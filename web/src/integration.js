import {esc,money} from './core.js';

export function readiness(model,wallet,prices,base,backendReady,now=Date.now()){
 const reasons=[];let reserve=null;
 if(!base)reasons.push('尚未配置公网 API 地址');
 if(!model?.capabilities?.chat||!model?.capabilities?.tools)reasons.push('当前模型未声明对话和工具能力');
 const catalog=prices?.catalog;
 if(wallet?.currency!=='CNY'||wallet?.scale!==1e9||catalog?.currency!=='CNY')reasons.push('钱包或价格币种不符合 CNY 约定');
 if(!Number.isFinite(Date.parse(catalog?.expires_at))||Date.parse(catalog.expires_at)<=now)reasons.push('价目表无效或已过期');
 const rows=catalog?.prices?.filter(p=>p.model===model?.id)||[];
 if(!rows.length)reasons.push('当前模型没有有效价格');
 const amount=n=>Number.isSafeInteger(n)&&n>=0;
 if(!amount(wallet?.available_nano))reasons.push('余额数据不符合约定');
 if(rows.length){
  if(rows.some(p=>!amount(p.max_input_tokens)||p.max_input_tokens<1||!amount(p.input_nano_per_million)||!amount(p.output_nano_per_million)))reasons.push('价格数据不符合约定');
  else {const max=rows.reduce((a,p)=>{const value=(BigInt(p.max_input_tokens)*BigInt(p.input_nano_per_million)+4096n*BigInt(p.output_nano_per_million)+999999n)/1000000n;return value>a?value:a;},0n)*3n;reserve=max.toString();if(amount(wallet?.available_nano)&&(wallet.available_nano<=0||BigInt(wallet.available_nano)<max))reasons.push('余额不足以覆盖最多三次尝试的冻结上限');}
 }
 if(!backendReady)reasons.push('网关尚未就绪');
 return {ok:!reasons.length,reasons,reserve_nano:reserve};
}
export function clientProfiles(base,model){
 const id=model.id,root=base.replace(/\/v1\/?$/,''),vision=model.capabilities?.vision===true;
 const entry={npm:'@ai-sdk/openai-compatible',name:'AgentMesh',options:{baseURL:root+'/v1',apiKey:'{env:AGENTMESH_API_KEY}'},models:{[id]:{name:id,limit:{context:8192,output:4096},modalities:{input:vision?['text','image']:['text'],output:['text']}}}};
 const opencode={$schema:'https://opencode.ai/config.json',model:'agentmesh/'+id,small_model:'agentmesh/'+id,enabled_providers:['agentmesh'],provider:{agentmesh:entry}};
 const claude={env:{ANTHROPIC_BASE_URL:root,ANTHROPIC_MODEL:id,ANTHROPIC_DEFAULT_HAIKU_MODEL:id,ANTHROPIC_DEFAULT_SONNET_MODEL:id,ANTHROPIC_DEFAULT_OPUS_MODEL:id,CLAUDE_CODE_MAX_OUTPUT_TOKENS:'4096',MAX_THINKING_TOKENS:'0',CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:'1'}};
 return {opencode,claude};
}
export function renderIntegration(a){return a.head('CLIENT CONNECTION','客户端接入','先检查当前工作空间，再生成独立的客户端配置。检查不会调用模型。')+a.panel('连接检查',`<div class="form"><label>获准模型<select id="client-model" disabled><option>正在获取目录…</option></select></label><button class="btn" id="client-check">重新检查</button></div><div id="client-status" class="inline-result"></div>`)+`<div class="columns wide" style="margin-top:20px">${['opencode','claude'].map(id=>a.panel(id==='opencode'?'OpenCode':'Claude Code',`<pre id="${id}-config">请先通过连接检查。</pre><button class="btn" id="copy-${id}" disabled>复制配置</button><button class="btn" id="download-${id}" disabled>下载 JSON</button>`)).join('')}</div><div class="notice" style="margin-top:20px">网关应用 Key 由你在客户端本机设置：OpenCode 使用 AGENTMESH_API_KEY；Claude Code 使用 ANTHROPIC_API_KEY，并清除冲突的 ANTHROPIC_AUTH_TOKEN。此处检查的是控制台专用 Key；新签发应用 Key 仍应使用仓库 client-config.py 单独验证。配置不会嵌入 Key。</div><p class="subtle">使用独立 OPENCODE_CONFIG 或 claude --settings 指向下载文件。历史实测仅覆盖 Read 工具往返，配置预检不能证明所有 Shell、MCP 或长任务兼容。</p>`;}
export async function mountIntegration(a,view){
 const $=s=>document.querySelector(s),current=()=>a.isCurrent(view);let models=[],wallet,prices,ready=false,profiles=null,busy=false;
 const render=()=>{
  const model=models.find(m=>m.id===$('#client-model').value),result=readiness(model,wallet,prices,a.config.api_base_url,ready);
  profiles=result.ok?clientProfiles(a.config.api_base_url,model):null;
  $('#client-status').innerHTML=result.ok?`<div class="notice">配置前置检查通过 · 可用余额 ${esc(money(wallet.available_nano))} · 冻结上限 ${esc(result.reserve_nano)} nano-CNY。${a.preview?'当前为示例结果。':'尚未执行客户端模型调用。'}</div>`:`<div class="error">${result.reasons.map(esc).join('<br>')}</div>`;
  for(const id of ['opencode','claude']){$('#'+id+'-config').textContent=profiles?JSON.stringify(profiles[id],null,2):'请修正上述条件后重新检查。';$('#copy-'+id).disabled=!profiles;$('#download-'+id).disabled=!profiles;}
 };
 const load=async()=>{if(busy)return;busy=true;profiles=null;$('#client-check').disabled=true;for(const id of ['opencode','claude']){$('#copy-'+id).disabled=true;$('#download-'+id).disabled=true;}try{
  const responses=await Promise.all([a.backend('/v1/models',{signal:view.signal}),a.backend('/v1/billing/balance',{signal:view.signal}),a.backend('/v1/billing/prices',{signal:view.signal}),a.request('/api/readiness',{signal:view.signal})]);if(!current())return;
  const selected=$('#client-model').value;models=responses[0].data.data;if(!Array.isArray(models))throw Error('模型目录格式错误');wallet=responses[1].data;prices=responses[2].data;ready=responses[3].data.status==='ready';
  $('#client-model').innerHTML=models.length?models.map(m=>`<option value="${esc(m.id)}">${esc(m.id)}${m.capabilities?.tools?' · 工具':''}</option>`).join(''):'<option value="">暂无获准模型</option>';if(models.some(m=>m.id===selected))$('#client-model').value=selected;else $('#client-model').value=models.find(m=>m.capabilities?.tools)?.id||models[0]?.id||'';$('#client-model').disabled=!models.length;render();
 }catch(e){if(current()&&e.name!=='AbortError')a.error($('#client-status'),e);}finally{busy=false;if(current())$('#client-check').disabled=false;}};
 $('#client-model').onchange=render;$('#client-check').onclick=load;
 for(const id of ['opencode','claude']){$('#copy-'+id).onclick=async()=>{if(!profiles)return;try{await navigator.clipboard.writeText(JSON.stringify(profiles[id],null,2));}catch{a.error($('#client-status'),Error('复制失败，请下载配置文件。'));}};$('#download-'+id).onclick=()=>{if(profiles)a.download(profiles[id],id==='opencode'?'opencode.json':'claude-settings.json');};}await load();
}
