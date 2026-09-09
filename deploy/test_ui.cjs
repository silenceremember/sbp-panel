const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {test} = require('node:test');
const source = fs.readFileSync('internal/panel/web/app.js', 'utf8');
const restoreSource = source.slice(source.indexOf('async function restoreConfiguration('), source.indexOf('function setupUpdater()'));

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
