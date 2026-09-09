const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {test} = require('node:test');
const source = fs.readFileSync('internal/panel/web/app.js', 'utf8');
const restoreSource = source.slice(source.indexOf('async function restoreConfiguration('), source.indexOf('function setupUpdater()'));
const suggestName = vm.runInNewContext(source.slice(source.indexOf('function suggestedDeviceName('), source.indexOf('function deviceDialog(')) + ';suggestedDeviceName');
const exportSource = source.slice(source.indexOf('const nameSlug ='), source.indexOf('function configurationDialog('));

test('group Markdown export keeps ordered codes, multiline profiles and the final check link', async () => {
  const calls = [];
  let output;
  const profile = '[Interface]\nPrivateKey = synthetic\n[Peer]\nPublicKey = test';
  const exportCodes = vm.runInNewContext(exportSource + ';exportGroupCodes', {
    window:{location:{origin:'https://panel.example'}}, state:{server_country:'Ireland'}, DEVICE_METHOD_NAMES:{xray:'Xray',amneziawg:'AmneziaWG'},
    downloadFile:(name,content,type) => { output = {name,content,type}; },
    api:async path => {
      calls.push(path);
      if (path === '/api/groups/7/devices') return {devices:[{id:8,name:'Phone [home]',method:'xray',enabled:true},{id:9,name:'Laptop',method:'amneziawg',enabled:false}]};
      if (path === '/api/devices/8/credential') return {credential:'vless://synthetic'};
      if (path === '/api/devices/9/credential') return {credential:profile};
      throw new Error(`Unexpected request: ${path}`);
    }
  });
  await exportCodes({id:7,name:'Family Group'});
  assert.equal(output.name, 'Ireland_Family_Group.md');
  assert.match(output.type, /^text\/markdown/);
  assert(output.content.includes('## Phone \\[home\\]'));
  assert(output.content.indexOf('vless://synthetic') < output.content.indexOf(profile));
  assert(output.content.includes('AmneziaWG · Disabled\n\n```text\n' + profile + '\n```'));
  assert(output.content.endsWith('## Check link\n\nhttps://panel.example/check/Family_Group\n'));
  assert.equal(calls.length, 3);
});

test('failed credential fetch does not download an incomplete group export', async () => {
  let downloaded = false;
  const exportCodes = vm.runInNewContext(exportSource + ';exportGroupCodes', {
    window:{location:{origin:'https://panel.example'}}, DEVICE_METHOD_NAMES:{},
    downloadFile:() => { downloaded = true; },
    api:async path => {
      if (path === '/api/groups/7/devices') return {devices:[{id:8}]};
      throw new Error('credential unavailable');
    }
  });
  await assert.rejects(exportCodes({id:7,name:'Family'}), /credential unavailable/);
  assert.equal(downloaded, false);
});

function dialogFixture() {
  const calls = [];
  const nodes = Object.fromEntries(['#dialog', '#dialog-body', '#dialog-form', '#dialog-title', '#dialog-ok', '#dialog-form [value="cancel"]', '[data-cookie-choice]', '[data-clear-bypass]', '[data-bypass-rooms]', 'input'].map(key => [key, {disabled:false}]));
  nodes['#dialog'].close = () => { nodes['#dialog'].open = false; };
  nodes['.drop'] = {querySelector: key => nodes[key], addEventListener() {}};
  nodes['#dialog-body'].querySelector = key => nodes[key];
  nodes['#dialog-ok'].isConnected = true;
  nodes['#dialog-ok'].closest = () => nodes['#dialog'];
  const context = vm.createContext({
    document:{querySelector:key => nodes[key]}, FormData, pendingActions:new Set(), dialogGeneration:0,
    BYPASS_COMPONENT_SETTINGS:{test:{provider:'telemost',label:'Yandex Telemost'}},
    openDialog:dialog => { dialog.open = true; }, renderBypassRooms() {}, escapeHTML:String,
    setButtonLabel:(button,label) => { button.textContent = label; },
    notify() {}, notifyError:error => { throw error; }, confirm:() => true,
    api:async (path, options = {}) => { calls.push({path,...options}); return {rooms:[]}; }
  });
  vm.runInContext(source.slice(source.indexOf('function fileDropHTML('), source.indexOf('function openDialog(')) + source.slice(source.indexOf('async function runPendingAction('), source.indexOf('const localInput =')) + source.slice(source.indexOf('function bypassSettingsDialog('), source.indexOf('function componentTextSettingsDialog(')), context);
  return {nodes,calls,context,open:() => context.bypassSettingsDialog({id:'test'}),submit:value => nodes['#dialog-form'].onsubmit({submitter:{value},preventDefault(){}})};
}

test('provider cookie selection and clearing stay local until Save, and Cancel discards them', async () => {
  const f = dialogFixture();
  const mutations = () => f.calls.filter(call => call.method);
  f.open();
  const file = new Blob(['[{"name":"test","value":"synthetic"}]'], {type:'application/json'});
  file.name = 'cookies.json';
  f.nodes.input.files = [file];
  f.nodes.input.onchange();
  assert.equal(f.nodes['#dialog-ok'].disabled, false);
  assert.equal(mutations().length, 0);
  await f.submit('cancel');
  f.open();
  await f.submit('default');
  assert.equal(mutations().length, 0);
  f.nodes.input.onchange();
  await f.submit('default');
  assert.equal(mutations()[0].method, 'POST');
  assert.equal(await mutations()[0].body.get('cookies').text(), await file.text());
  f.open();
  f.nodes['[data-clear-bypass]'].onclick();
  await f.submit('cancel');
  f.open();
  await f.submit('default');
  assert.equal(mutations().length, 1);
  f.nodes['[data-clear-bypass]'].onclick();
  await f.submit('default');
  assert.equal(mutations()[1].method, 'DELETE');
});

test('new dialogs reset actions and late saves cannot change the next dialog buttons', async () => {
  const {nodes,context} = dialogFixture();
  const button = nodes['#dialog-ok'];
  const cancel = nodes['#dialog-form [value="cancel"]'];
  button.disabled = cancel.disabled = cancel.hidden = true;
  context.setDialogAction('Save');
  assert.equal(button.disabled || cancel.disabled || cancel.hidden, false);
  let finish;
  const pending = context.runPendingAction('save', button, 'Saving', () => new Promise(resolve => { finish = resolve; }));
  context.dialogGeneration++;
  context.setDialogAction('Restore');
  button.disabled = true;
  finish();
  await pending;
  assert.equal(button.textContent, 'Restore');
  assert.equal(button.disabled, true);
});

test('connection names use country, group and short protocol with case-insensitive suffixes', () => {
  const group = {id:1,name:'Admin'};
  const devices = [{group_id:1,name:'Ireland - Admin - Amnezia'}, {group_id:1,name:'ireland - admin - amnezia2'}, {group_id:2,name:'Ireland - Admin - Xray'}];
  assert.equal(suggestName('Ireland',group,'amneziawg-native',devices),'Ireland - Admin - Amnezia3');
  for (const [method,label] of [['xray','Xray'],['xray-xhttp','XHTTP'],['bypass-wb','WB'],['bypass-vk','VK']]) assert.equal(suggestName('Ireland',group,method,devices),`Ireland - Admin - ${label}`);
});

function fixture(external = false, failInstall = false) {
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({path, ...options});
    if (path === '/api/discovery') return {components: [{id: 'docker', installed: true}, {id: 'xray', installed: false, external}]};
    if (path === '/api/state') return {groups: [{id: 4, name: 'Old'}]};
    if (path === '/api/components/xray/install' && failInstall) throw new Error('download failed');
    if (path === '/api/groups') return {id: 8};
    if (path === '/api/groups/8/devices') return {id: 9};
    return {ok: true};
  };
  const restore = vm.runInNewContext(restoreSource + ';restoreConfiguration', {api, FormData, Blob, document: {querySelector: () => ({})}, watchJob: async () => {}});
  return {restore, calls};
}
const configuration = {components:['docker','xray'], settings:{xray:'{}'}, cookies:{}, groups:[{name:'Family',contact:'',unlimited:false,expires_at:'2030-02-01T00:00:00Z',devices:[{name:'Phone',method:'xray',enabled:false,fingerprint:'firefox'}]}]};

test('restore replaces old groups, installs components and preserves dates and disabled state', async () => {
  const {restore,calls} = fixture();
  await restore(configuration, () => {});
  assert(calls.some(c => c.path === '/api/groups/4' && c.method === 'DELETE'));
  const install = calls.findIndex(c => c.path === '/api/components/xray/install');
  const create = calls.findIndex(c => c.path === '/api/groups/8/devices');
  assert(install >= 0 && create > install);
  assert.equal(calls.find(c => c.path === '/api/groups').body.ExpiresAt, configuration.groups[0].expires_at);
  assert.equal(calls[create].body.Fingerprint, 'firefox');
  assert.equal(calls.find(c => c.path === '/api/devices/9/enabled').body.Enabled, false);
});
test('external component blocks replacement before deleting groups', async () => {
  const {restore,calls} = fixture(true);
  await assert.rejects(restore(configuration, () => {}), /external/);
  assert(!calls.some(c => c.method === 'DELETE'));
});
test('restore uploads provider cookies through the multipart API', async () => {
  const {restore,calls} = fixture();
  const cookies = [{name:'test',value:'synthetic'}];
  await restore({...configuration,cookies:{vk:cookies}}, () => {});
  const upload = calls.find(c => c.path === '/api/bypass/vk/credentials' && c.method === 'POST');
  assert(upload.body instanceof FormData);
  assert.deepEqual(JSON.parse(await upload.body.get('cookies').text()), cookies);
  assert(!calls.some(c => c.path === '/api/bypass/vk/credentials' && c.method === 'DELETE'));
});
test('failed installation stops before issuing new profiles', async () => {
  const {restore,calls} = fixture(false,true);
  await assert.rejects(restore(configuration, () => {}), /download failed/);
  assert(!calls.some(c => c.path === '/api/groups' && c.method === 'POST'));
});
