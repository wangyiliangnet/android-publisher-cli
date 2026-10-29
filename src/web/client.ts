export const clientScript = String.raw`
'use strict';
const $ = (selector, root = document) => root.querySelector(selector);
const main = $('#main');
const token = $('meta[name="publisher-token"]').content;
const names = {huawei:'华为',honor:'荣耀',xiaomi:'小米',oppo:'OPPO',vivo:'vivo',tencent:'应用宝'};
const channels = Object.keys(names);
const credentialSpecs = {
  huawei:[{key:'service_account_json',label:'服务账号 JSON',material:'JSON'}],
  honor:[{key:'client_secret',label:'client_secret'}],
  xiaomi:[{key:'api_secret',label:'API 密钥（api_secret）'},{key:'public_key_pem',label:'API 公钥或证书 PEM',material:'PEM'},{key:'test_account_json',label:'审核测试账号 JSON',material:'JSON',optional:true}],
  oppo:[{key:'client_secret',label:'client_secret'}],
  vivo:[{key:'access_secret',label:'access_secret'}],
  tencent:[{key:'api_secret',label:'API 密钥（api_secret）'}],
};
const credentialHints = {
  input:'凭据输入格式无效。请重新选择已配置的渠道账号，并填写对应材料。',
  fields:'请填写当前渠道的必需凭据字段，同一字段只选择一种输入方式。',
  file:'请使用属于当前用户、权限为 0600 的本机文件绝对路径，或切换到直接粘贴内容。',
  format:'JSON 或 PEM 材料格式无效。请使用完整原文并保留原有换行，再重新输入。',
  account:'账号尚未关联应用。请先保存应用和对应渠道的账号资料，再重新接入。',
};
const state = {view:'apps',apps:[],app:null,appDraft:null,releases:[],release:null,wizard:null,accounts:[],diagnostic:null,protocols:[],busy:false,progress:'',notice:null};
let navigationPending = false;
let toastTimer;
const esc = value => String(value ?? '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
const formatDate = value => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}) : '—';
const bytes = value => value >= 1024*1024 ? (value/1024/1024).toFixed(1)+' MB' : Math.ceil(value/1024)+' KB';
const icons = {
  apps:'<rect x="2.5" y="2.5" width="11" height="11" rx="2"/><path d="M5 6h6M5 9h4"/>',
  repo:'<path d="M3 1.8h9v12.4H3a1.5 1.5 0 0 1-1.5-1.5V3.3A1.5 1.5 0 0 1 3 1.8Zm-1.5 9.5H12M4 4.5h5"/>',
  plus:'<path d="M8 2v12M2 8h12"/>',
  history:'<path d="M2 8a6 6 0 1 0 1.5-4M2 2v4h4M8 4.5V8l2.5 1.5"/>',
  shield:'<path d="m8 1 5.5 2v4c0 3-2 5.5-5.5 8C4.5 12.5 2.5 10 2.5 7V3L8 1Z"/><path d="m5.5 8 1.7 1.7L11 6"/>',
  settings:'<path d="m6 1-.5 2-2 .8-1.8-.5L.7 5l1.4 1.5v3L.7 11l1 1.7 1.8-.5 2 .8.5 2h4l.5-2 2-.8 1.8.5 1-1.7-1.4-1.5v-3L15.3 5l-1-1.7-1.8.5-2-.8L10 1H6Z"/><circle cx="8" cy="8" r="2.2"/>',
  check:'<path d="m2.5 8 3.5 3.5 7.5-7.5"/>',
  upload:'<path d="M8 11V2m-3 3 3-3 3 3M2 10v4h12v-4"/>',
  arrow:'<path d="M2 8h12m-5-5 5 5-5 5"/>',
  terminal:'<rect x="1.5" y="2.5" width="13" height="11" rx="2"/><path d="m4 6 2 2-2 2m4 0h3"/>',
  refresh:'<path d="M13 6a5.3 5.3 0 0 0-9-3L2 5m0-4v4h4m-3 5a5.3 5.3 0 0 0 9 3l2-2m0 4v-4h-4"/>',
  search:'<circle cx="6.5" cy="6.5" r="4.5"/><path d="m10 10 4 4"/>',
  file:'<path d="M3 1.5h6L13 6v8.5H3v-13Zm6 0V6h4M5 9h6m-6 3h4"/>',
};
const icon = name => '<svg viewBox="0 0 16 16" aria-hidden="true">'+(icons[name] || icons.apps)+'</svg>';
const badge = (text, kind = '') => '<span class="badge '+kind+'">'+esc(text)+'</span>';
const btn = (text, action, kind = '', extra = '') => '<button type="button" class="btn '+kind+'" data-action="'+action+'" '+extra+(state.busy?' disabled':'')+'>'+text+'</button>';
const link = (text, hash, kind = '') => '<a class="btn '+kind+'" href="'+esc(hash)+'">'+text+'</a>';
const selectedApp = id => state.apps.find(app => app.project_id === id);
const packageName = app => app.package_name || (app.channels.find(row => ['xiaomi','oppo','vivo'].includes(row.channel)) || {}).app_id || '商店应用';
const localTime = () => {const d=new Date();d.setMinutes(d.getMinutes()-d.getTimezoneOffset());return d.toISOString().slice(0,16);};
function toast(message) { const target=$('#toast'); target.textContent=message;target.hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>target.hidden=true,4200); }
async function api(action, payload = {}) {
  const response = await fetch('/api/action',{method:'POST',headers:{'Content-Type':'application/json','X-Publisher-Token':token},body:JSON.stringify({action,payload})});
  const result = await response.json();
  if (!response.ok) {const error=new Error(action==='accounts.update'?'凭据接入未完成。':result.error || '请求未完成，请重试。');if(action==='accounts.update'&&['input','fields','file','format','account'].includes(result.code))error.credentialIssue=result.code;throw error;}
  return result;
}
async function upload(file, purpose) {
  const headers={'Content-Type':'application/octet-stream','X-Publisher-Token':token,'X-Filename':encodeURIComponent(file.name)};
  if(purpose)headers['X-Upload-Purpose']=purpose;
  const response = await fetch('/api/upload',{method:'POST',headers,body:file});
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || '文件读取失败。');
  return result.path;
}
async function work(progress, callback) {
  if (state.busy) return;
  $('#toast').hidden=true;clearTimeout(toastTimer);
  state.busy=true;state.progress=progress;state.notice=null;render();
  try { await callback(); }
  catch (error) { state.notice={type:'error',text:error.message || '操作未完成。'}; }
  finally { state.busy=false;state.progress='';render();if(navigationPending){navigationPending=false;await navigate();} }
}
function notice() {
  let html=state.progress?'<div class="progress-label" role="status"><span class="spinner"></span>'+esc(state.progress)+'</div>':'';
  if(state.notice)html+='<div class="notice '+esc(state.notice.type)+'" role="alert">'+esc(state.notice.text)+'</div>';
  return html;
}
function heading(title, description, actions = '') {
  return '<div class="page-heading"><div><h1>'+title+'</h1>'+(description?'<p>'+description+'</p>':'')+'</div><div class="page-actions">'+actions+'</div></div>';
}
function empty(title, description, action = '') {
  return '<div class="panel"><div class="empty-state"><div class="empty-icon">'+icon('repo')+'</div><h2>'+esc(title)+'</h2><p>'+esc(description)+'</p>'+action+'</div></div>';
}
function renderNavigation() {
  const isApps=['apps','app','appForm','publish','release'].includes(state.view);
  const navItem=(view,label,symbol,count='')=>'<a href="#/'+view+'" class="nav-item '+((view==='apps'?isApps:state.view===view)?'active':'')+'">'+icon(symbol)+label+(count?'<span class="count">'+count+'</span>':'')+'</a>';
  $('#navigation').innerHTML=navItem('apps','应用','apps',String(state.apps.length))+'<div class="nav-heading">管理</div>'+navItem('accounts','账号与凭据','shield')+navItem('doctor','环境诊断','terminal')+navItem('protocols','渠道能力','history');
}
function render() {
  renderNavigation();
  const views={apps:renderApps,app:renderApp,appForm:renderAppForm,publish:renderWizard,release:renderRelease,accounts:renderAccounts,doctor:renderDoctor,protocols:renderProtocols};
  main.innerHTML=(views[state.view]||renderApps)();
  document.querySelectorAll('button').forEach(button=>{if(state.busy && !button.classList.contains('modal-close'))button.disabled=true;});
}
function renderApps() {
  let html=heading('应用','管理商店资料，发布各渠道版本，跟踪每一次更新。',link(icon('plus')+' 新建应用','#/app/new','primary'))+notice();
  if(!state.apps.length)return html+empty('配置你的第一个应用','保存应用和渠道账号资料，之后每次发版都可以复用。首次配置无需提供 APK。',link(icon('plus')+' 新建应用','#/app/new','primary'))+
    '<div class="onboarding-note">'+icon('terminal')+' 也可以在终端运行 <code>android-release app add</code>。两种入口共用本机配置和发布记录。</div>';
  html+='<div class="toolbar"><div class="search-box">'+icon('search')+'<input type="search" id="app-search" placeholder="查找应用或包名…" aria-label="查找应用"></div><span class="small-muted">'+state.apps.length+' 个应用配置</span></div><div class="cards">';
  for(const app of state.apps)html+='<article class="app-card" data-app-search="'+esc((app.project_id+' '+packageName(app)).toLowerCase())+'"><div class="app-card-top">'+icon('repo')+'<h2><a href="#/app/'+encodeURIComponent(app.project_id)+'">'+esc(app.project_id)+'</a></h2>'+badge('本机')+'</div><div class="package">'+esc(packageName(app))+'</div><div class="channel-tags">'+app.channels.map(row=>badge(names[row.channel])).join('')+'</div><div class="app-card-footer"><span>'+app.channels.length+' 个渠道</span>'+link('准备发布 '+icon('arrow'),'#/publish/'+encodeURIComponent(app.project_id),'small')+'</div></article>';
  return html+'</div><div class="onboarding-note">默认向应用已配置的全部渠道发布。每个渠道的 APK 由你明确选择，提交前统一核对。</div>';
}
function appBreadcrumb(app, suffix) {
  return '<div class="breadcrumbs"><a href="#/apps">应用</a><span>/</span><a href="#/app/'+encodeURIComponent(app.project_id)+'">'+esc(app.project_id)+'</a>'+(suffix?'<span>/</span>'+esc(suffix):'')+'</div>';
}
function renderApp() {
  const app=state.app;if(!app)return notice();
  let html=appBreadcrumb(app,'')+heading(esc(app.project_id)+' '+badge('本机'),esc(packageName(app)),link(icon('shield')+' 账号与凭据','#/accounts')+btn(icon('check')+' 检查渠道访问','check-app')+link(icon('plus')+' 新建发布','#/publish/'+encodeURIComponent(app.project_id),'primary'))+notice();
  html+='<div class="tabs"><span class="tab active">'+icon('history')+' 发布记录 <span class="count">'+state.releases.length+'</span></span><a class="tab" href="#/app/'+encodeURIComponent(app.project_id)+'/settings">'+icon('settings')+' 应用配置</a></div>';
  if(!state.releases.length)return html+empty('发布你的第一个版本','为每个渠道选择 APK，再提供一份更新说明。工具会完成检查，并在上传前请你确认。',link('准备发布','#/publish/'+encodeURIComponent(app.project_id),'primary'));
  return html+releaseList(state.releases);
}
function releaseList(releases) {
  let html='<div class="panel"><div class="panel-header"><span>'+icon('history')+' 发布记录</span><span class="small-muted">最近更新在前</span></div><div class="table-wrap"><table><thead><tr><th>版本</th><th>渠道受理</th><th>审核 / 上架</th><th>创建时间</th><th></th></tr></thead><tbody>';
  for(const release of releases)html+='<tr><td><div class="row-title"><a href="#/release/'+encodeURIComponent(release.batch_id)+'"><strong>'+esc(release.version_name||release.version_code||'查看发布')+'</strong></a>'+badge(release.version_code||'版本记录')+'</div><div class="cell-note mono">'+esc(release.package_name||release.project_id)+'</div></td><td>'+badge((release.accepted_targets||0)+' / '+(release.total_targets||(release.targets||[]).length)+' 已受理',release.all_accepted?'success':release.recovery_required?'warn':'')+(release.recovery_required?'<div class="cell-note">存在待核对请求</div>':'')+'</td><td><span class="small-muted">'+(release.approved_targets||0)+' 审核通过 · '+(release.live_targets||0)+' 已上架</span></td><td class="small-muted">'+formatDate(release.created_at)+'</td><td>'+link('查看详情','#/release/'+encodeURIComponent(release.batch_id),'small')+'</td></tr>';
  return html+'</tbody></table></div></div>';
}
function field(label, name, value='', hint='', type='text', required=false, disabled=false) {
  return '<div class="field"><label for="'+name+'">'+esc(label)+(required?'<span class="required">*</span>':'')+'</label><input type="'+type+'" id="'+name+'" name="'+name+'" value="'+esc(value)+'" '+(required?'required ':'')+(disabled?'disabled ':'')+'autocomplete="off">'+(hint?'<span class="hint">'+esc(hint)+'</span>':'')+'</div>';
}
function renderAppForm() {
  const app=state.appDraft||state.app||{project_id:'',package_name:'',channels:channels.map(channel=>({channel,account:channel+'-main',app_id:''}))};
  const editing=Boolean(state.app);
  let html=(editing?appBreadcrumb(state.app,'应用配置'):'<div class="breadcrumbs"><a href="#/apps">应用</a><span>/</span>新建应用</div>')+heading(editing?'应用配置':'新建应用',editing?'修改长期资料。已有发布继续使用冻结时的配置。':'一次配置应用与渠道资料，供日常发版复用。')+notice();
  html+='<form id="app-form" class="form-layout"><div class="panel"><div class="panel-header">应用信息</div><div class="panel-body"><div class="form-grid">'+field('应用名称','project_id',app.project_id,'本机使用的名称，例如 demo。', 'text',true,editing)+field('应用包名','package_name',packageName(app)==='商店应用'?'':packageName(app),'例如 com.example.app，须与本次应用的渠道包一致。','text',true)+'</div><p class="footnote">首次配置只保存长期资料。各渠道 APK 在发版时提供。</p></div></div><div class="toolbar"><h2>渠道与商店应用</h2><span class="small-muted">选择需要配置的渠道</span></div>';
  for(const channel of channels){
    const saved=app.channels.find(row=>row.channel===channel);
    const row=saved||{channel,account:channel+'-main',app_id:''};
    const clientLabels={honor:'client_id',xiaomi:'userName',oppo:'client_id',vivo:'access_key',tencent:'uid'};
    html+='<section class="channel-config"><div class="channel-config-header"><label class="checkbox-label"><input type="checkbox" name="channel" value="'+channel+'" '+(saved?'checked':'')+'>'+esc(names[channel])+'</label><span class="hint">'+esc(channel)+'</span></div><div class="channel-fields" '+(!saved?'hidden':'')+'><div class="form-grid">'+field('账号备注',channel+'_account',row.account,'同一个渠道账号沿用相同备注。');
    if(['huawei','honor','tencent'].includes(channel))html+=field('商店 App ID',channel+'_app_id',row.app_id,'从该应用的商店后台复制。');
    if(clientLabels[channel])html+=field(clientLabels[channel],channel+'_client_id',row.client_id||(channel==='oppo'?row.account:''),'渠道 API 的公开账号标识，密钥在账号与凭据中另行接入。');
    html+='</div>';
    if(channel==='xiaomi')html+='<div class="form-grid">'+field('应用图标绝对路径',channel+'_icon_path',row.icon_path||'','小米 API 需要此图标。可填写本机路径或选择图标文件。')+field('隐私政策地址',channel+'_privacy_url',row.privacy_url||'','HTTPS 网页地址，不带查询参数或片段。','url')+'</div><div class="field"><label class="btn small" for="xiaomi-icon">'+icon('upload')+' 选择图标文件</label><input type="file" id="xiaomi-icon" class="file-native" accept="image/png,image/jpeg"></div>';
    if(row.language_id)html+='<p class="small-muted">保留更新说明语言：'+esc(row.language_id)+'</p>';
    html+='</div></section>';
  }
  html+='<div class="notice">'+icon('shield')+' 保存后可在“账号与凭据”直接接入并验证密钥，日常发布会自动复用本机保存的凭据。</div><div class="form-actions"><button class="btn primary" type="submit">'+(editing?'保存应用配置':'创建应用配置')+'</button>'+link('取消',editing?'#/app/'+encodeURIComponent(app.project_id):'#/apps')+'</div></form>';
  return html;
}
function steps(current) {
  return '<ol class="steps" aria-label="发布步骤">'+['发布材料','渠道预检','核对提交','受理结果'].map((label,index)=>'<li class="step '+(index+1===current?'active':index+1<current?'done':'')+'"><span class="step-number">'+(index+1<current?'✓':index+1)+'</span>'+label+'</li>').join('')+'</ol>';
}
function renderWizard() {
  const wizard=state.wizard;if(!wizard)return notice();
  const app=selectedApp(wizard.appId);if(!app)return notice();
  let html=appBreadcrumb(app,'发布更新')+heading('发布应用更新','为 '+esc(app.project_id)+' 提交新版本。默认包含全部已配置渠道。')+steps(wizard.step)+notice();
  if(wizard.step===1)return html+renderMaterials(app,wizard);
  if(wizard.step===2)return html+renderPreflight(wizard);
  if(wizard.step===3)return html+renderConfirmation(wizard);
  return html+renderReleaseDetails(wizard.release,true);
}
function renderMaterials(app,wizard) {
  let html='';
  if(wizard.previous)html+='<div class="notice"><strong>发现尚未完成的发布：'+esc(wizard.previous.version_name||wizard.previous.version_code)+'</strong><p>可以继续已有记录，复用原 APK、更新说明和渠道进度。</p>'+btn('继续这次发布','continue-record','small','data-release="'+esc(wizard.previous.batch_id)+'"')+'</div>';
  html+='<div class="panel"><div class="panel-header"><span>渠道 APK</span><span class="small-muted">逐渠道指定，文件名不参与匹配</span></div>';
  for(const row of app.channels){
    const included=wizard.channels.includes(row.channel);const file=wizard.files[row.channel];
    html+='<div class="artifact-row"><div class="artifact-channel"><label class="checkbox-label"><input type="checkbox" data-select-channel="'+row.channel+'" '+(included?'checked':'')+'>'+esc(names[row.channel])+'</label></div><div class="artifact-input">';
    if(included)html+='<label class="file-picker" tabindex="0" role="button" for="apk-'+row.channel+'">'+icon('file')+'<span><span class="file-name">'+(file?esc(file.name):'选择 '+esc(names[row.channel])+' 对应的 APK')+'</span><span class="file-meta">'+(file?' · '+bytes(file.size):' · .apk 文件')+'</span></span><span class="spacer"></span><span class="small-muted">浏览</span></label><input class="file-native" type="file" id="apk-'+row.channel+'" data-apk="'+row.channel+'" accept=".apk">';
    else html+='<span class="small-muted">本次跳过此渠道</span>';
    html+='</div></div>';
  }
  html+='</div><div class="panel"><div class="panel-header">更新说明</div><div class="panel-body"><div class="field"><label for="release-notes">全部选定渠道共用</label><textarea id="release-notes" placeholder="输入或粘贴本次更新说明…" maxlength="500">'+esc(wizard.notes)+'</textarea><div class="counter" id="notes-count">'+[...wizard.notes].length+' / '+(wizard.channels.includes('vivo')?'200':'500')+' 字符</div></div><label class="btn small" for="notes-file">'+icon('file')+' 从文本文件读取</label><input class="file-native" type="file" id="notes-file" accept=".txt,.md,text/plain,text/markdown"></div></div><div class="form-actions">'+btn(icon('check')+' 检查并准备发布','prepare','primary')+link('取消','#/app/'+encodeURIComponent(app.project_id))+'</div><p class="footnote">此阶段检查并保存本次材料。上传和提审将在核对内容并确认后开始。</p>';
  return html;
}
function renderPreflight(wizard) {
  if(!wizard.release)return '<div class="loading-state">正在准备发布…</div>';
  const targets=wizard.release.targets||[];
  let html=wizard.relatedRelease?'<div class="notice warn">相同产物已有发布记录，当前更新说明或应用配置发生了变化。本次已建立新的发布记录，请重新核对并明确确认提交范围。<br><a href="#/release/'+encodeURIComponent(wizard.relatedRelease)+'">查看原发布记录</a></div>':'';
  html+='<div class="notice">检查各渠道账号和商店现状。失败的渠道可在修复后重新检查，也可明确取消勾选，调整本次提交范围。</div><div class="panel"><div class="panel-header"><span>渠道就绪情况</span>'+btn(icon('refresh')+' 重新检查','preflight','small')+'</div><div class="table-wrap"><table><thead><tr><th>本次纳入</th><th>渠道</th><th>账号 / 商店应用</th><th>预检结果</th></tr></thead><tbody>';
  for(const row of targets){
    const accepted=row.execution_phase==='submitted';
    const error=wizard.errors[row.channel];const blocked=row.recovery_required;
    const checked=wizard.channels.includes(row.channel);
    html+='<tr><td><input type="checkbox" aria-label="纳入'+esc(names[row.channel])+'" data-review-channel="'+row.channel+'" '+(checked?'checked ':'')+(accepted?'disabled':'')+'></td><td><strong>'+esc(names[row.channel])+'</strong></td><td>'+esc(row.account||'')+'<div class="cell-note mono">'+esc(row.app_id||'')+'</div></td><td>'+badge(accepted?'已经受理':blocked?'待核对':error?'未就绪':row.execution_phase==='preflighted'?'检查通过':'等待检查',accepted?'success':error||blocked?'warn':row.execution_phase==='preflighted'?'success':'')+(error?'<div class="cell-note">'+esc(error)+'</div>':'')+(blocked?'<div class="cell-note">存在未决请求，先核对结果。</div>':'')+'</td></tr>';
  }
  html+='</tbody></table></div></div><div class="form-actions">'+btn('核对提交内容 '+icon('arrow'),'review','primary')+link('返回发布记录','#/app/'+encodeURIComponent(wizard.appId))+'</div>';
  return html;
}
function renderConfirmation(wizard) {
  const release=wizard.release,plan=wizard.plan;
  if(!release||!plan)return '<div class="notice warn">请先完成渠道预检。</div>';
  const completed=release.targets.filter(row=>plan.selected_channels.includes(row.channel)&&row.execution_phase==='submitted').length;
  let html='<div class="notice warn"><strong>确认后将上传 APK 并提交审核。</strong> 审核通过后立即上架。请核对应用、渠道与对应产物。</div><div class="panel"><div class="panel-header">本次提交内容</div><div class="panel-body"><dl class="summary-grid"><div class="summary-item"><dt>应用</dt><dd>'+esc(wizard.appId)+'</dd></div><div class="summary-item"><dt>包名</dt><dd class="mono">'+esc(plan.package_name||release.package_name)+'</dd></div><div class="summary-item"><dt>版本</dt><dd>'+esc(plan.version_name||release.version_name)+'（'+esc(plan.version_code||release.version_code)+'）</dd></div><div class="summary-item"><dt>提交范围</dt><dd>'+plan.selected_channels.map(channel=>esc(names[channel])).join('、')+'</dd></div><div class="summary-item wide"><dt>签名证书 SHA-256</dt><dd class="mono">'+esc(plan.signer_sha256||'')+'</dd></div></dl></div><div class="table-wrap"><table><thead><tr><th>渠道</th><th>账号 / 商店应用</th><th>APK</th><th>产物 SHA-256</th></tr></thead><tbody>';
  for(const channel of plan.selected_channels){
    const row=release.targets.find(target=>target.channel===channel)||{};const artifact=row.artifact||{};const identity=(plan.target_accounts||[]).find(target=>target.channel===channel)||row;
    html+='<tr><td><strong>'+esc(names[channel])+'</strong>'+(row.execution_phase==='submitted'?'<div class="cell-note">'+badge('已受理，将跳过','success')+'</div>':'')+'</td><td>'+esc(identity.account||'')+'<div class="cell-note mono">'+esc(identity.app_id||'')+'</div></td><td>'+esc(artifact.filename||'已冻结 APK')+(artifact.size?'<div class="cell-note">'+bytes(artifact.size)+'</div>':'')+'</td><td class="mono">'+esc((plan.artifact_sha256[channel]||'').slice(0,16))+'…</td></tr>';
  }
  html+='</tbody></table></div></div><div class="panel"><div class="panel-header">更新说明</div><div class="panel-body"><p class="notes">'+esc(plan.release_notes||release.release_notes)+'</p></div></div>'+(completed?'<div class="notice">'+completed+' 个已受理渠道将保留原结果，工具只继续尚未完成的步骤。</div>':'')+'<div class="form-actions">'+btn(completed?'确认并继续 '+(plan.selected_channels.length-completed)+' 个未完成渠道':'确认并提交 '+plan.selected_channels.length+' 个渠道','submit','primary')+btn('返回调整范围','back-preflight')+'</div><p class="footnote">点击确认即授权向上述渠道提交此应用和这些产物。受理结果、审核结果和上架结果分别记录。</p>';
  return html;
}
function statusBadge(row, kind) {
  if(kind==='execution'){
    if(row.execution_phase==='submitted')return badge('已受理','success');
    if(row.recovery_required)return badge('待核对','warn');
    return badge(({prepared:'待提交',preflighted:'已就绪',submitted:'已受理',unknown:'待核对'})[row.execution_phase]||'未确认',row.execution_phase==='submitted'?'success':row.execution_phase==='unknown'?'warn':'');
  }
  if(kind==='review'){
    const value=row.review_state;
    return badge(({in_review:'审核中',approved:'审核通过',rejected:'审核拒绝'})[value]||(row.query_support&&row.query_support.review===false?'待人工核对':'未确认'),value==='approved'?'success':value==='rejected'?'danger':value==='in_review'?'info':'');
  }
  if(kind==='availability'){
    const value=row.availability_state;
    return badge(({live:'已上架',not_live:'未上架'})[value]||(row.query_support&&row.query_support.availability===false?'待人工核对':'未确认'),value==='live'?'success':'');
  }
  return badge(({ok:'成功',failed:'本次失败',never:'尚未查询'})[row.query_status]||'尚未查询',row.query_status==='failed'?'danger':row.query_status==='ok'?'success':'');
}
function manualRows(release, row) {
  const records=row.manual_observations||(release.manual_observations||[]).filter(record=>record.channel===row.channel);
  if(!records.length)return '';
  const record=records[records.length-1];
  const review=record.review_state||record.review;
  const availability=record.availability_state||record.availability;
  return '<div class="manual-record"><strong>人工核对</strong> · '+esc(({in_review:'审核中',approved:'审核通过',rejected:'审核拒绝'})[review]||'审核未确认')+' / '+esc(({live:'已上架',not_live:'未上架'})[availability]||'上架未确认')+'<br>'+esc(record.source||'商店后台')+' · '+formatDate(record.observed_at||record.recorded_at)+'</div>';
}
function renderReleaseDetails(release, inWizard=false) {
  if(!release)return '';
  const total=release.total_targets||(release.targets||[]).length;
  let html='<div class="stats"><div class="stat"><div class="stat-label">提交受理</div><div class="stat-value">'+(release.accepted_targets||0)+' <span>/ '+total+' 个渠道</span></div></div><div class="stat"><div class="stat-label">审核通过 · 自动确认</div><div class="stat-value">'+(release.approved_targets||0)+' <span>/ '+total+'</span></div></div><div class="stat green"><div class="stat-label">已上架 · 自动确认</div><div class="stat-value">'+(release.live_targets||0)+' <span>/ '+total+'</span></div></div><div class="stat"><div class="stat-label">需要核对</div><div class="stat-value">'+(release.targets||[]).filter(row=>row.recovery_required).length+' <span>个渠道</span></div></div></div>';
  if(release.all_accepted)html+='<div class="notice success">所有选定渠道均已受理本次提交。审核与上架进度请继续查询。</div>';
  else if(release.recovery_required)html+='<div class="notice warn">部分请求结果未决。请先按渠道核对原请求，工具会保留记录并阻止冲突提交。</div>';
  else html+='<div class="notice">本次发布尚有渠道未受理。可继续尚未完成的渠道，已受理的渠道会自动跳过。</div>';
  html+='<div class="panel"><div class="panel-header"><span>'+icon('history')+' 各渠道进度</span>'+btn(icon('refresh')+' 查询最新结果','refresh','small')+'</div><div class="table-wrap"><table><thead><tr><th>渠道 / 产物</th><th>提交</th><th>审核</th><th>上架</th><th>查询情况</th><th></th></tr></thead><tbody>';
  for(const row of release.targets||[]){
    html+='<tr><td><strong>'+esc(names[row.channel])+'</strong><div class="cell-note">'+esc((row.artifact||{}).filename||'')+'</div></td><td>'+statusBadge(row,'execution')+((release.errors||{})[row.channel]?'<div class="cell-note">'+esc(release.errors[row.channel])+'</div>':'')+'</td><td>'+statusBadge(row,'review')+manualRows(release,row)+'</td><td>'+statusBadge(row,'availability')+'</td><td>'+statusBadge(row,'query')+(row.last_success_at?'<div class="cell-note">上次成功<br>'+formatDate(row.last_success_at)+'</div>':'')+(row.last_error?'<div class="cell-note">'+esc(row.last_error)+'</div>':'')+'</td><td>'+btn('人工核对','manual','small','data-channel="'+row.channel+'"')+'</td></tr>';
  }
  html+='</tbody></table></div></div>';
  const recovery=(release.targets||[]).flatMap(row=>(row.recovery||[]).map(issue=>({...issue,channel:row.channel})));
  if(recovery.length)html+='<div class="panel"><div class="panel-header">待核对事项</div><ul class="check-list">'+recovery.map(issue=>'<li><span>'+badge(names[issue.channel],'warn')+'</span><div class="check-content"><div class="check-title">'+esc(issue.message||'请求结果待核对')+'</div><div class="check-detail">'+esc(issue.next_action||'请在商店后台核对本次版本与原操作。')+'</div></div></li>').join('')+'</ul></div>';
  html+='<div class="panel"><div class="panel-header">发布材料</div><div class="panel-body"><dl class="summary-grid"><div class="summary-item"><dt>版本</dt><dd>'+esc(release.version_name||'')+'（'+esc(release.version_code||'')+'）</dd></div><div class="summary-item"><dt>创建时间</dt><dd>'+formatDate(release.created_at)+'</dd></div><div class="summary-item wide"><dt>包名</dt><dd class="mono">'+esc(release.package_name||'')+'</dd></div><div class="summary-item wide"><dt>更新说明</dt><dd><p class="notes">'+esc(release.release_notes||'')+'</p></dd></div></dl></div></div>';
  html+='<div class="form-actions">'+(!release.all_accepted?btn('继续未完成渠道 '+icon('arrow'),'continue','primary'):'')+(release.recovery_required?btn('只读核对未决请求','reconcile'):'')+link('返回应用','#/app/'+encodeURIComponent(release.project_id||state.wizard?.appId||''))+'</div>';
  return html;
}
function renderRelease() {
  const release=state.release;if(!release)return notice();
  const app=selectedApp(release.project_id)||{project_id:release.project_id||'应用'};
  return appBreadcrumb(app,'发布详情')+heading('版本 '+esc(release.version_name||release.version_code||''),esc(release.package_name||'')+' · '+formatDate(release.created_at))+notice()+renderReleaseDetails(release);
}
function renderAccounts() {
  let html=heading('账号与凭据','在此接入与更新渠道凭据，CLI 与 Web 自动复用本机保存的账号。')+notice()+'<div class="notice">'+icon('shield')+' 凭据验证通过后安全保存在本机。账号资料和发布进度由两个入口共用。</div>';
  if(!state.accounts.length)return html+empty('暂无渠道账号','先创建应用并选择渠道，再接入对应账号的凭据。',link('新建应用','#/app/new','primary'));
  html+='<div class="panel"><div class="panel-header">本机渠道账号</div><div class="table-wrap"><table><thead><tr><th>渠道 / 账号</th><th>凭据</th><th>引用应用</th><th>操作</th></tr></thead><tbody>';
  for(const account of state.accounts)html+='<tr><td><strong>'+esc(names[account.channel])+'</strong><div class="cell-note">'+esc(account.account)+'</div></td><td>'+badge(account.saved?'已保存':'待接入',account.saved?'success':'warn')+'</td><td>'+account.apps.map(app=>'<a href="#/app/'+encodeURIComponent(app)+'">'+esc(app)+'</a>').join('、')+'</td><td><div class="flex wrap">'+btn(account.saved?'更新凭据':'接入凭据','credential-update','small'+(account.saved?'':' primary'),'data-channel="'+esc(account.channel)+'" data-account="'+esc(account.account)+'"')+btn('清除凭据','forget','small danger','data-channel="'+esc(account.channel)+'" data-account="'+esc(account.account)+'"')+'</div></td></tr>';
  return html+'</tbody></table></div></div><p class="footnote">也可通过终端更新：<code>android-release account update &lt;渠道&gt; &lt;账号备注&gt;</code></p>';
}
function credentialField(spec) {
  const key=spec.key,label=esc(spec.label),required=spec.optional?'（可选）':'<span class="required">*</span>';
  if(!spec.material)return '<div class="field"><label for="credential-'+key+'">'+label+required+'</label><div class="credential-input"><input type="password" id="credential-'+key+'" data-credential-secret="'+key+'" autocomplete="new-password" spellcheck="false" autocapitalize="off" autocorrect="off" maxlength="65536"><button type="button" class="btn small" data-action="credential-reveal" data-control="credential-'+key+'" aria-pressed="false">显示内容</button></div></div>';
  const content='<div class="field credential-material" data-credential-material="'+key+'"><label for="credential-mode-'+key+'">'+label+required+'</label><select id="credential-mode-'+key+'" data-credential-mode="'+key+'"><option value="file">使用本机私有文件路径</option><option value="text">直接粘贴 '+esc(spec.material)+' 内容</option></select><div class="credential-method" data-credential-file-panel="'+key+'"><label class="field-label" for="credential-file-'+key+'">本机绝对路径</label><input type="text" id="credential-file-'+key+'" data-credential-file="'+key+'" autocomplete="off" spellcheck="false" autocorrect="off" maxlength="4096" placeholder="/绝对路径/'+(spec.material==='PEM'?'public-key.pem':'account.json')+'"><span class="hint">文件须属于当前用户，权限为 0600。服务端会先检查权限，再读取材料。</span></div><div class="credential-method" data-credential-text-panel="'+key+'" hidden><div class="credential-label-row"><label class="field-label" for="credential-text-'+key+'">'+esc(spec.material)+' 内容</label><button type="button" class="btn small" data-action="credential-reveal" data-control="credential-text-'+key+'" aria-pressed="false">显示内容</button></div><textarea id="credential-text-'+key+'" class="credential-secret masked" data-credential-secret="'+key+'" autocomplete="off" spellcheck="false" autocapitalize="off" autocorrect="off" maxlength="65536" rows="5" placeholder="粘贴完整 '+esc(spec.material)+' 内容，保留原有换行。"></textarea><span class="hint">内容默认遮蔽，可临时显示核对。提交或关闭后会清空输入。</span></div></div>';
  return spec.optional?'<details class="credential-optional"><summary>'+label+'（可选）</summary>'+content+'</details>':content;
}
function syncCredentialModes(form) {
  for(const select of form.querySelectorAll('[data-credential-mode]')){
    const field=select.dataset.credentialMode;
    form.querySelector('[data-credential-file-panel="'+field+'"]').hidden=select.value!=='file';
    form.querySelector('[data-credential-text-panel="'+field+'"]').hidden=select.value!=='text';
  }
}
function clearCredentialForm(form) {
  if(!form)return;
  for(const control of form.querySelectorAll('input,textarea')){control.value='';control.defaultValue='';control.removeAttribute('value');if(control.tagName==='TEXTAREA')control.textContent='';if(control.matches('input[data-credential-secret]'))control.type='password';if(control.tagName==='TEXTAREA')control.classList.add('masked');}
  form.reset();syncCredentialModes(form);
  for(const button of form.querySelectorAll('[data-action="credential-reveal"]')){button.textContent='显示内容';button.setAttribute('aria-pressed','false');}
}
function closeModal() {
  const form=$('#credentials-form');
  const channel=form?.dataset.channel,account=form?.dataset.account;
  clearCredentialForm(form);
  $('#modal').replaceChildren();
  if(channel&&account)[...document.querySelectorAll('[data-action="credential-update"]')].find(button=>button.dataset.channel===channel&&button.dataset.account===account)?.focus();
}
function openCredentials(channel,account) {
  closeModal();
  $('#toast').hidden=true;clearTimeout(toastTimer);
  const row=state.accounts.find(item=>item.channel===channel&&item.account===account);
  if(!row||!credentialSpecs[channel])return;
  $('#modal').innerHTML='<div class="modal-overlay"><section class="modal-dialog credential-dialog" role="dialog" aria-modal="true" aria-labelledby="credential-title" aria-describedby="credential-context"><div class="modal-heading"><h2 id="credential-title">'+esc(names[channel])+' · '+(row.saved?'更新凭据':'接入凭据')+'</h2><button type="button" class="modal-close" data-action="close-modal" aria-label="关闭">×</button></div><div class="modal-content"><div id="credential-context" class="credential-context"><div><span class="small-muted">渠道账号</span><strong>'+esc(account)+'</strong></div><div><span class="small-muted">关联应用</span><span>'+row.apps.map(esc).join('、')+'</span></div></div><p class="footnote">验证渠道访问权限后安全保存，后续发布会自动复用。应用配置、发布记录和查询结果共用同一账号。</p><form id="credentials-form" autocomplete="off" data-channel="'+esc(channel)+'" data-account="'+esc(account)+'"><div id="credential-error" class="notice error" role="alert" hidden></div>'+credentialSpecs[channel].map(credentialField).join('')+'<div id="credential-progress" class="progress-label" role="status" hidden><span class="spinner"></span>正在验证并保存凭据…</div><div class="form-actions"><button type="submit" class="btn primary" id="credential-save">验证并保存</button>'+btn('取消','close-modal')+'</div></form></div></section></div>';
  const form=$('#credentials-form');
  [...form.querySelectorAll('input,textarea,select')].find(control=>control.getClientRects().length)?.focus();
}
async function saveCredentials(form) {
  if(state.busy)return;
  let payload;
  let failed=false;
  let saved=false;
  const rejectInput=code=>{const error=new Error('credential input');error.credentialIssue=code;throw error;};
  const setError=text=>{if(!form.isConnected)return;const error=form.querySelector('#credential-error');error.textContent=text;error.hidden=false;};
  try{
    const credentials={},files={};
    payload={channel:form.dataset.channel,account:form.dataset.account,credentials,files};
    const specs=credentialSpecs[payload.channel];
    if(!specs)rejectInput('account');
    for(const spec of specs){
      const file=spec.material&&form.querySelector('[data-credential-mode="'+spec.key+'"]').value==='file';
      const control=file?form.querySelector('[data-credential-file="'+spec.key+'"]'):form.querySelector('[data-credential-secret="'+spec.key+'"]');
      const value=file?control.value.trim():control.value;
      if(!value.trim()){if(spec.optional)continue;rejectInput('fields');}
      if(file&&!value.startsWith('/'))rejectInput('file');
      const bytes=new TextEncoder().encode(value),tooLarge=bytes.length>(file?4096:65536);bytes.fill(0);
      if(tooLarge)rejectInput('input');
      (file?files:credentials)[spec.key]=value;
    }
    clearCredentialForm(form);
    form.querySelector('#credential-error').hidden=true;
    form.querySelector('#credential-progress').hidden=false;
    for(const control of form.querySelectorAll('input,textarea,select,button'))control.disabled=true;
    state.busy=true;state.progress='正在验证渠道凭据…';state.notice=null;render();
    const result=await api('accounts.update',payload);
    if(result.updated!==true)throw new Error('凭据保存尚未确认。');
    saved=true;
    closeModal();
    state.accounts=(await api('accounts.list')).accounts||[];
    toast('凭据已验证并安全保存。');
  }catch(error){
    failed=true;
    clearCredentialForm(form);
    const message=saved?'凭据已保存，账号列表刷新未完成，请刷新页面查看。':(credentialHints[error.credentialIssue]||'凭据验证或保存未完成。请检查材料、账号权限和钥匙串后重新输入。')+' 已输入内容已清空。';
    if(form.isConnected)setError(message);else state.notice={type:saved?'warn':'error',text:saved?message:'凭据接入未完成，请重新打开账号凭据表单。'};
  }finally{
    if(payload){for(const values of [payload.credentials,payload.files])for(const key of Object.keys(values||{})){values[key]='';delete values[key];}}
    state.busy=false;state.progress='';
    if(form.isConnected){form.querySelector('#credential-progress').hidden=true;for(const control of form.querySelectorAll('input,textarea,select,button'))control.disabled=false;}
    render();
    if(failed&&form.isConnected)[...form.querySelectorAll('input,textarea,select')].find(control=>control.getClientRects().length)?.focus();
    if(navigationPending){navigationPending=false;await navigate();}
  }
}
function renderDoctor() {
  const result=state.diagnostic;
  let html=heading('环境诊断','检查发布所需的本机组件，直接安装或修复检查环境。',btn(icon('refresh')+' 重新检查','doctor')+btn(icon('upload')+' 安装 / 修复检查组件','doctor-repair',result?.ready?'':'primary'))+notice();
  if(!result)return html;
  html+='<div class="notice '+(result.ready?'success':'warn')+'">'+(result.ready?'发布组件检查通过。可以准备应用更新。':'部分组件需要处理。请按下方提示修复后重新检查。')+'</div>';
  if(result.version||result.runtime||result.platform||result.arch)html+='<div class="panel"><div class="panel-header">版本与运行信息</div><div class="panel-body"><dl class="summary-grid"><div class="summary-item"><dt>CLI 版本</dt><dd>'+esc(result.version||'—')+'</dd></div><div class="summary-item"><dt>Node.js</dt><dd>'+esc(result.runtime||'—')+'</dd></div><div class="summary-item"><dt>运行平台</dt><dd>'+esc(result.platform||'—')+'</dd></div><div class="summary-item"><dt>CPU 架构</dt><dd>'+esc(result.arch||'—')+'</dd></div></dl></div></div>';
  html+='<div class="panel"><div class="panel-header">组件检查</div><ul class="check-list">';
  for(const check of result.checks||[])html+='<li>'+badge(({ok:'通过',missing:'缺失',failed:'失败',blocked:'待检查'})[check.status]||check.status,check.status==='ok'?'success':'warn')+'<div class="check-content"><div class="check-title">'+esc(check.name||check.id)+'</div><div class="check-detail">'+esc(check.message||'')+(check.path?'<p class="mono">'+esc(check.path)+'</p>':'')+(check.fixes||[]).map(fix=>'<p>'+esc(fix)+'</p>').join('')+'</div></div></li>';
  return html+'</ul></div>';
}
function renderProtocols() {
  let html=heading('渠道能力','了解当前能够自动确认的结果，以及需要后台核对的事项。')+notice()+'<div class="notice warn">六渠道以模拟接口验证为主。实际账号权限与公开上架结果，仍需使用获准的测试应用逐渠道验证。</div><div class="panel"><div class="table-wrap"><table><thead><tr><th>渠道</th><th>审核查询</th><th>上架确认</th><th>更新说明上限</th><th>关联条件</th></tr></thead><tbody>';
  for(const row of state.protocols)html+='<tr><td><strong>'+esc(row.name||names[row.channel])+'</strong></td><td>'+badge(row.review_query?'支持':'后台核对',row.review_query?'success':'')+'</td><td>'+badge(row.availability_query?'支持':'后台核对',row.availability_query?'success':'')+'</td><td>'+esc(row.release_notes_max)+' 字符</td><td><span class="small-muted">'+esc(row.query_note||'')+'</span></td></tr>';
  return html+'</tbody></table></div></div><p class="footnote">受理、审核与上架分别确认。无法关联本次版本的信息保留为未确认，响应丢失的写入继续阻止冲突提交。</p>';
}
function newWizard(app) { return {appId:app.project_id,step:1,channels:app.channels.map(row=>row.channel),files:{},uploaded:{},notes:'',release:null,plan:null,errors:{}}; }
async function navigate() {
  closeModal();
  if(state.busy){navigationPending=true;return;}
  $('#toast').hidden=true;clearTimeout(toastTimer);
  state.busy=true;state.progress='正在加载…';state.notice=null;render();
  try{
    state.apps=(await api('apps.list')).apps||[];
    const parts=(location.hash.replace(/^#\/?/,'')||'apps').split('/').map(decodeURIComponent);
    const section=parts[0];
    if(section==='app'&&parts[1]==='new'){state.view='appForm';state.app=null;state.appDraft=null;}
    else if(section==='app'&&parts[1]){
      state.app=selectedApp(parts[1]);if(!state.app)throw new Error('找不到此应用，请返回应用列表。');
      if(parts[2]==='settings'){state.view='appForm';state.appDraft=null;}
      else{state.view='app';state.releases=(await api('releases.list',{app_id:state.app.project_id})).releases||[];}
    }else if(section==='publish'&&parts[1]){
      const app=selectedApp(parts[1]);if(!app)throw new Error('请先配置应用。');
      state.view='publish';if(!state.wizard||state.wizard.appId!==app.project_id){state.wizard=newWizard(app);const previous=(await api('releases.list',{app_id:app.project_id})).releases||[];state.wizard.previous=previous.find(release=>!release.all_accepted);}
    }else if(section==='release'&&parts[1]){
      state.view='release';const result=await api('releases.get',{release_id:parts[1],refresh:true});state.release={...result.release,errors:result.errors||{}};
    }else if(section==='accounts'){state.view='accounts';state.accounts=(await api('accounts.list')).accounts||[];}
    else if(section==='doctor'){state.view='doctor';state.diagnostic=await api('doctor');}
    else if(section==='protocols'){state.view='protocols';const result=await api('protocols');state.protocols=result.channels||[];}
    else{state.view='apps';state.wizard=null;}
  }catch(error){state.notice={type:'error',text:error.message};}
  finally{state.busy=false;state.progress='';render();}
}
async function prepareRelease() {
  const wizard=state.wizard;
  if(!wizard.channels.length)throw new Error('请至少选择一个渠道。');
  const missing=wizard.channels.filter(channel=>!wizard.files[channel]);
  if(missing.length)throw new Error('请为以下渠道选择 APK：'+missing.map(channel=>names[channel]).join('、')+'。');
  const length=[...wizard.notes].length,max=wizard.channels.includes('vivo')?200:500;
  if([...wizard.notes.trim()].length<5||length>max)throw new Error('更新说明须至少 5 个字符，当前选定渠道最多 '+max+' 个字符。');
  const apks={};
  for(const channel of wizard.channels){
    const file=wizard.files[channel];
    state.progress='正在读取 '+names[channel]+' APK…';render();
    if(wizard.uploaded[channel]?.file!==file)wizard.uploaded[channel]={file,path:await upload(file)};
    apks[channel]=wizard.uploaded[channel].path;
  }
  state.progress='正在校验并冻结发布材料…';render();
  const result=await api('releases.prepare',{app_id:wizard.appId,apks,release_notes:wizard.notes,channels:wizard.channels});
  wizard.release=result.release;wizard.relatedRelease=result.related_release||null;
  if(result.duplicate)toast('已找到相同材料的发布记录，继续使用已有进度。');
  if(wizard.release.all_accepted){wizard.step=4;return;}
  wizard.step=2;wizard.plan=null;
  await preflightRelease();
}
async function preflightRelease() {
  const wizard=state.wizard;
  if(!wizard.channels.length)throw new Error('请至少选择一个尚未完成的渠道。');
  state.progress='正在检查渠道账号和商店现状…';render();
  const result=await api('releases.preflight',{release_id:wizard.release.batch_id,channels:wizard.channels});
  wizard.release=result.release;wizard.errors=result.errors||{};wizard.plan=null;
}
function activeRelease() { return state.view==='publish'?state.wizard?.release:state.release; }
async function continueRelease(existing) {
  const release=existing||activeRelease(),app=selectedApp(release.project_id);
  if(!app)throw new Error('请先配置此应用。');
  const pending=release.targets.filter(row=>row.execution_phase!=='submitted').map(row=>row.channel);
  if(!pending.length){toast('本次发布的全部渠道已经受理。');return;}
  state.wizard={...newWizard(app),step:2,channels:release.targets.map(row=>row.channel),release};
  state.view='publish';history.replaceState(null,'','#/publish/'+encodeURIComponent(app.project_id));
  await preflightRelease();
}
function openManual(channel) {
  closeModal();
  const release=activeRelease();
  $('#modal').innerHTML='<div class="modal-overlay"><section class="modal-dialog" role="dialog" aria-modal="true" aria-labelledby="manual-title"><div class="modal-heading"><h2 id="manual-title">记录 '+esc(names[channel])+' 人工核对</h2><button type="button" class="modal-close" data-action="close-modal" aria-label="关闭">×</button></div><div class="modal-content"><p class="small-muted">'+esc(release.package_name)+' · '+esc(release.version_name)+'（'+esc(release.version_code)+'）</p><div class="notice">请先在商店后台核对上述版本。此记录会标示为人工核对，未决请求仍须取得可验证证据后处理。</div><form id="manual-form" data-channel="'+channel+'"><div class="form-grid"><div class="field"><label for="manual-review">审核结果</label><select id="manual-review" name="review_state"><option value="unknown">尚未确认</option><option value="in_review">审核中</option><option value="approved">审核通过</option><option value="rejected">审核拒绝</option></select></div><div class="field"><label for="manual-live">上架结果</label><select id="manual-live" name="availability_state"><option value="unknown">尚未确认</option><option value="live">已上架</option><option value="not_live">未上架</option></select></div></div>'+field('核对时间','observed_at',localTime(),'','datetime-local',true)+field('结果来源','source','商店开发者后台','填写核对入口或本机证据说明。','text',true)+'<div class="field"><label for="manual-note">补充说明</label><textarea id="manual-note" name="note" maxlength="2000" placeholder="记录与本次版本关联的依据…" rows="3"></textarea></div><div class="form-actions"><button type="submit" class="btn primary">保存人工核对记录</button>'+btn('取消','close-modal')+'</div></form></div></section></div>';
  $('#manual-review').focus();
}
async function saveApp(form) {
  const data=new FormData(form),editing=Boolean(state.app);
  const selection=data.getAll('channel');
  if(!selection.length)throw new Error('请至少选择一个渠道。');
  const app={schema_version:2,project_id:editing?state.app.project_id:String(data.get('project_id')||'').trim(),package_name:String(data.get('package_name')||'').trim(),channels:[]};
  const validId=value=>/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(value);
  if(!validId(app.project_id))throw new Error('应用名称须以字母或数字开头，只能包含字母、数字、点、横线和下划线，最多 128 个字符。');
  if(!/^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$/.test(app.package_name))throw new Error('请输入有效包名，例如 com.example.app。');
  for(const channel of selection){
    const row={channel,account:String(data.get(channel+'_account')||'').trim(),app_id:['xiaomi','oppo','vivo'].includes(channel)?app.package_name:String(data.get(channel+'_app_id')||'').trim()};
    if(!validId(row.account))throw new Error(names[channel]+'：请填写有效的账号备注。');
    if(!validId(row.app_id))throw new Error(names[channel]+'：请填写有效的商店 App ID。');
    const client=String(data.get(channel+'_client_id')||'').trim();if(client)row.client_id=client;
    if(['honor','xiaomi','oppo','vivo','tencent'].includes(channel)&&!validId(client))throw new Error(names[channel]+'：请填写有效的 API 账号标识。');
    if(channel==='xiaomi'){row.icon_path=String(data.get('xiaomi_icon_path')||'').trim();row.privacy_url=String(data.get('xiaomi_privacy_url')||'').trim();}
    const previous=state.app?.channels.find(item=>item.channel===channel);if(previous?.language_id)row.language_id=previous.language_id;
    app.channels.push(row);
  }
  state.appDraft=app;
  await work('正在保存应用配置…',async()=>{
    await api('apps.save',{app,replace:editing});
    state.apps=(await api('apps.list')).apps||[];state.app=selectedApp(app.project_id);state.view='app';state.releases=(await api('releases.list',{app_id:app.project_id})).releases||[];
    state.notice={type:'success',text:'应用配置已保存。接下来可在“账号与凭据”直接接入各渠道，再检查访问权限。'};history.replaceState(null,'','#/app/'+encodeURIComponent(app.project_id));toast('应用配置已保存。');
  });
}
document.addEventListener('submit',event=>{
  event.preventDefault();const form=event.target;
  if(state.busy)return;
  if(form.id==='credentials-form'){void saveCredentials(form);return;}
  if(form.id==='app-form')void saveApp(form).catch(error=>{state.notice={type:'error',text:error.message};render();});
  if(form.id==='manual-form'){
    const values=new FormData(form),channel=form.dataset.channel;
    const payload={release_id:activeRelease().batch_id,channel,review_state:values.get('review_state'),availability_state:values.get('availability_state'),observed_at:new Date(String(values.get('observed_at'))).toISOString(),source:String(values.get('source')||''),note:String(values.get('note')||'')};
    $('#modal').innerHTML='';
    void work('正在保存人工核对记录…',async()=>{const result=await api('releases.manual',payload);if(state.view==='publish')state.wizard.release=result.release;else state.release=result.release;toast('人工核对记录已保存。');});
  }
});
document.addEventListener('input',event=>{
  const target=event.target;
  if(target.matches('[data-credential-secret],[data-credential-file]')){const error=target.closest('form').querySelector('#credential-error');if(error)error.hidden=true;}
  if(target.id==='app-search')document.querySelectorAll('[data-app-search]').forEach(card=>card.hidden=!card.dataset.appSearch.includes(target.value.toLowerCase()));
  if(target.id==='release-notes'&&state.wizard){state.wizard.notes=target.value;$('#notes-count').textContent=[...target.value].length+' / '+(state.wizard.channels.includes('vivo')?'200':'500')+' 字符';}
});
document.addEventListener('change',async event=>{
  const target=event.target;
  if(target.dataset.credentialMode){const form=target.closest('form'),key=target.dataset.credentialMode;for(const control of form.querySelectorAll('[data-credential-file="'+key+'"],[data-credential-secret="'+key+'"]'))control.value='';const secret=form.querySelector('[data-credential-secret="'+key+'"]');secret.classList.add('masked');const reveal=form.querySelector('[data-control="'+secret.id+'"]');reveal.textContent='显示内容';reveal.setAttribute('aria-pressed','false');form.querySelector('#credential-error').hidden=true;syncCredentialModes(form);return;}
  if(target.name==='channel'){target.closest('.channel-config').querySelector('.channel-fields').hidden=!target.checked;return;}
  if(target.dataset.selectChannel){const wizard=state.wizard,channel=target.dataset.selectChannel;wizard.channels=target.checked?[...wizard.channels,channel]:wizard.channels.filter(item=>item!==channel);render();return;}
  if(target.dataset.reviewChannel){const wizard=state.wizard,channel=target.dataset.reviewChannel;wizard.channels=target.checked?[...wizard.channels,channel]:wizard.channels.filter(item=>item!==channel);wizard.plan=null;return;}
  if(target.dataset.apk){const file=target.files[0];if(file){state.wizard.files[target.dataset.apk]=file;render();}return;}
  if(target.id==='notes-file'){
    const file=target.files[0];if(!file)return;
    try{if(file.size>64*1024)throw new Error('更新说明文件超过 64 KiB。');state.wizard.notes=new TextDecoder('utf-8',{fatal:true}).decode(await file.arrayBuffer());render();}
    catch(error){toast(error.message||'请使用 UTF-8 文本文件。');}
  }
  if(target.id==='xiaomi-icon'){
    const file=target.files[0];if(!file)return;
    target.disabled=true;
    try{const filePath=await upload(file,'app-icon');$('#xiaomi_icon_path').value=filePath;toast('图标已保存在本机。');}
    catch(error){toast(error.message);}
    finally{target.disabled=false;}
  }
});
document.addEventListener('click',event=>{
  const anchor=event.target.closest('a');
  if(anchor&&state.busy){event.preventDefault();toast('当前操作正在执行，请稍候。');return;}
  const button=event.target.closest('[data-action]');if(!button)return;
  const action=button.dataset.action;
  if(action==='close-modal'){closeModal();return;}
  if(state.busy)return;
  if(action==='credential-update'){openCredentials(button.dataset.channel,button.dataset.account);return;}
  if(action==='credential-reveal'){const control=document.getElementById(button.dataset.control);if(!control)return;const visible=button.getAttribute('aria-pressed')!=='true';if(control.tagName==='INPUT')control.type=visible?'text':'password';else control.classList.toggle('masked',!visible);button.setAttribute('aria-pressed',String(visible));button.textContent=visible?'隐藏内容':'显示内容';return;}
  if(action==='manual'){openManual(button.dataset.channel);return;}
  if(action==='back-preflight'){state.wizard.step=2;state.wizard.plan=null;render();return;}
  if(action==='forget'){
    if(!confirm('清除 '+names[button.dataset.channel]+' 账号 '+button.dataset.account+' 的本机凭据？引用此账号的应用将需要重新接入。'))return;
    void work('正在清除本机凭据…',async()=>{await api('accounts.forget',{channel:button.dataset.channel,account:button.dataset.account});state.accounts=(await api('accounts.list')).accounts||[];toast('本机凭据已清除。');});return;
  }
  if(action==='prepare'){void work('正在准备发布材料…',prepareRelease);return;}
  if(action==='check-app'){void work('正在检查渠道访问权限…',async()=>{const result=await api('apps.check',{app_id:state.app.project_id});const errors=Object.entries(result.errors||{});state.notice=errors.length?{type:'warn',text:errors.map(([channel,error])=>names[channel]+'：'+error).join('；')}:{type:'success',text:'所有已配置渠道的访问检查通过。'};});return;}
  if(action==='continue-record'){void work('正在读取已有发布记录…',async()=>{const result=await api('releases.get',{release_id:button.dataset.release,refresh:false});await continueRelease(result.release);});return;}
  if(action==='preflight'){void work('正在检查渠道…',preflightRelease);return;}
  if(action==='review'){
    void work('正在生成提交摘要…',async()=>{
      const wizard=state.wizard;if(!wizard.channels.length)throw new Error('请至少选择一个渠道。');
      if(wizard.channels.some(channel=>wizard.errors[channel]||wizard.release.targets.find(row=>row.channel===channel)?.recovery_required))throw new Error('所选渠道仍有预检失败或待核对事项，请先处理或明确取消勾选。');
      const result=await api('releases.review',{release_id:wizard.release.batch_id,channels:wizard.channels});wizard.release=result.release;wizard.plan=result.plan;wizard.step=3;
    });return;
  }
  if(action==='submit'){
    void work('正在上传 APK 并提交审核，请保持终端与本页面运行…',async()=>{
      const wizard=state.wizard;if(!wizard.plan)throw new Error('请先核对提交内容。');
      const result=await api('releases.submit',{release_id:wizard.release.batch_id,approval_plan:wizard.plan.approval_plan});wizard.release={...result.release,errors:result.errors||{}};wizard.step=4;wizard.plan=null;
    });return;
  }
  if(action==='continue'){void work('正在加载未完成渠道…',continueRelease);return;}
  if(action==='refresh'){
    void work('正在查询各渠道最新结果…',async()=>{const result=await api('releases.get',{release_id:activeRelease().batch_id,refresh:true});const release={...result.release,errors:result.errors||{}};if(state.view==='publish')state.wizard.release=release;else state.release=release;});return;
  }
  if(action==='reconcile'){
    void work('正在读取可核对的远端证据…',async()=>{const result=await api('releases.reconcile',{release_id:activeRelease().batch_id});const release={...result.release,errors:result.errors||{}};if(state.view==='publish')state.wizard.release=release;else state.release=release;});return;
  }
  if(action==='doctor'){void work('正在检查本机组件…',async()=>{state.diagnostic=await api('doctor');});return;}
  if(action==='doctor-repair'){void work('正在安装并校验 APK 检查组件，请稍候…',async()=>{state.diagnostic=await api('doctor.repair');toast(state.diagnostic.ready?'检查组件已安装，环境检查通过。':'检查组件安装完成，请查看剩余诊断事项。');});return;}
});
document.addEventListener('keydown',event=>{
  if(event.key==='Escape')closeModal();
  if((event.key==='Enter'||event.key===' ')&&event.target.classList.contains('file-picker')){event.preventDefault();document.getElementById(event.target.htmlFor).click();}
  const dialog=$('.modal-dialog');
  if(event.key==='Tab'&&dialog){const controls=[...dialog.querySelectorAll('button,input,textarea,select,[tabindex]')].filter(control=>!control.disabled&&control.getClientRects().length);const first=controls[0],last=controls[controls.length-1];if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus();}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus();}}
});
window.addEventListener('pagehide',closeModal);
window.addEventListener('beforeunload',closeModal);
window.addEventListener('hashchange',()=>void navigate());
void navigate();
`;
