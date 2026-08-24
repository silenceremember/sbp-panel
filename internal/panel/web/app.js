const app = document.querySelector('#app');
let state = null;
let csrf = '';
let metricsTimer = null;
let metricsLoading = false;
let lastMetrics = null;
let updateInfo = null;
let updateCheckedAt = 0;
const deviceToggleJobs = new Map();
const pendingActions = new Set();
let viewGeneration = 0;
let metricsPollGeneration = 0;
let metricsRequestGeneration = 0;
let discoveryGeneration = 0;
let bypassRoomsGeneration = 0;
let dialogGeneration = 0;
let lifecycleWatchGeneration = 0;
let activeLifecycle = null;
let bypassRooms = [];
const pendingUpdateKey = 'sbp-pending-update';
const updateCacheKey = 'sbp-update-check-cache';
const updateCacheMilliseconds = 5 * 60 * 1000;
const minimumLoadingMilliseconds = 650;
const DEVICE_METHOD_OPTIONS = [
  ['xray', 'Xray · VLESS + REALITY'],
  ['xray-xhttp', 'Xray · VLESS + XHTTP + REALITY', 'Xray XHTTP'],
  ['amneziawg-app', 'AmneziaWG · AmneziaVPN profile (vpn://)'],
  ['amneziawg-native', 'AmneziaWG · Configuration file (.conf)'],
  ['bypass-wb', 'WB Stream'],
  ['bypass-telemost', 'Yandex Telemost'],
  ['bypass-dion', 'DION'],
  ['bypass-vk', 'VK Calls']
];
const DEVICE_METHOD_NAMES = Object.fromEntries(DEVICE_METHOD_OPTIONS.map(([id, label, shortName]) => [id, shortName || label.split(' · ')[0]]));
const BYPASS_COMPONENT_SETTINGS = {
  'bypass-wb': {provider: 'wbstream', label: 'WB Stream'},
  'bypass-telemost': {provider: 'telemost', label: 'Yandex Telemost'},
  'bypass-dion': {provider: 'dion', label: 'DION'},
  'bypass-vk': {provider: 'vk', label: 'VK Calls'}
};
const GLOBAL_COMPONENT_SETTINGS_NOTICE = 'Component settings are global desired server configuration. They are available before installation, are picked up by a later install, and remain available after installation.';

document.querySelector('#dialog')?.addEventListener('close', () => {
  dialogGeneration++;
  document.documentElement.classList.remove('dialog-open');
  document.body.classList.remove('dialog-open');
});

const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));

function imageReady(source) {
  const image = new Image();
  image.src = source;
  if (image.decode) return image.decode().catch(() => undefined);
  return new Promise(resolve => {
    image.onload = resolve;
    image.onerror = resolve;
  });
}

const dashboardVisualsReady = Promise.all([
  imageReady('/sbp-logo.svg'),
  imageReady('/sbp-logo-with-description.svg'),
  document.fonts?.ready || Promise.resolve()
]);

function deviceMethodLabel(device) {
  if (device.method !== 'amneziawg') return DEVICE_METHOD_NAMES[device.method] || device.method;
  return device.format === 'app' ? 'AmneziaWG · AmneziaVPN' : 'AmneziaWG · external client';
}

function stopMetricsPolling() {
  metricsPollGeneration++;
  clearTimeout(metricsTimer);
  metricsTimer = null;
}

function showLoading(message = 'Sit back and relax…') {
  stopMetricsPolling();
  app.innerHTML = `<section class="center loading-screen"><img class="loading-logo" src="/sbp-logo-with-description.svg" alt="SBP - Simple Bridge Panel"><div class="update-progress loading-progress" role="status" aria-live="polite"><div class="update-progress-copy"><span>${escapeHTML(message)}</span></div><div class="update-progress-track loading-progress-track" role="progressbar" aria-label="Loading"><span></span></div></div></section>`;
}

function showUpdateLoading(target, progress = 2, stage = 'Preparing update.') {
  viewGeneration++;
  stopMetricsPolling();
  const value = Math.max(0, Math.min(100, Number(progress) || 0));
  app.innerHTML = `<section class="center loading-screen update-loading">
    <img class="loading-logo" src="/sbp-logo-with-description.svg" alt="SBP - Simple Bridge Panel">
    <div class="update-progress" role="status" aria-live="polite">
      <div class="update-progress-copy"><span id="update-stage">${escapeHTML(stage)}</span><strong id="update-percent">${value}%</strong></div>
      <div class="update-progress-track" role="progressbar" aria-label="Update progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${value}"><span style="width:${value}%"></span></div>
      <small>Installing v${escapeHTML(target)}. The page will reopen automatically.</small>
    </div>
  </section>`;
}

function setUpdateProgress(progress, stage) {
  const value = Math.max(0, Math.min(100, Number(progress) || 0));
  const stageNode = document.querySelector('#update-stage');
  const percentNode = document.querySelector('#update-percent');
  const track = document.querySelector('.update-progress-track');
  const fill = track?.querySelector('span');
  if (stageNode) stageNode.textContent = stage || 'Installing update.';
  if (percentNode) percentNode.textContent = `${value}%`;
  if (track) track.setAttribute('aria-valuenow', String(value));
  if (fill) fill.style.width = `${value}%`;
}

async function copyText(value) {
  const text = String(value ?? '');
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const area = document.createElement('textarea');
    area.value = text;
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.append(area);
    area.select();
    document.execCommand('copy');
    area.remove();
  }
}

function notify(message, type = 'info', title = '', options = {}) {
  const root = document.querySelector('#notifications');
  if (!root) return;
  const text = String(message || 'Done');
  const noticeKey = `${type}\n${title}\n${text}\n${options.qr || ''}`;
  const duplicate = Array.from(root.children).find(item => item.dataset.noticeKey === noticeKey);
  if (duplicate) return duplicate;
  const labels = {success: 'Done', error: 'Error', warning: 'Warning', info: 'Information'};
  const notice = document.createElement('article');
  notice.className = `notification ${type}`;
  notice.dataset.noticeKey = noticeKey;
  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'notification-close';
  close.setAttribute('aria-label', 'Close notification');
  close.title = 'Close';
  close.textContent = '×';
  const bindCopy = button => {
    button.onclick = async () => {
      await copyText(text);
      button.textContent = 'Copied';
      setTimeout(() => { if (button.isConnected) button.textContent = 'Copy'; }, 1200);
    };
  };
  if (options.qr) {
    notice.classList.add('has-qr');
    const image = document.createElement('img');
    image.className = 'notification-qr';
    image.src = options.qr;
    image.alt = title ? `${title} QR code` : 'Credential QR code';

    const actions = document.createElement('div');
    actions.className = 'notification-actions notification-qr-actions';
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.className = 'button-secondary';
    copy.textContent = 'Copy';
    bindCopy(copy);
    const download = document.createElement('a');
    download.className = 'button button-secondary';
    download.href = options.qr;
    download.download = options.qrFilename || 'credential-qr.png';
    download.textContent = 'Download QR';
    actions.append(copy, download);

    notice.append(image, actions);
    notice.insertAdjacentHTML('beforeend', '<span class="notification-timer"></span>');
  } else {
    notice.innerHTML = `<div class="notification-content"><strong class="notification-title"></strong><p class="notification-message"></p></div><div class="notification-actions"><button type="button" class="button-secondary notification-copy">Copy</button></div><span class="notification-timer"></span>`;
    notice.querySelector('.notification-title').textContent = title || labels[type] || labels.info;
    notice.querySelector('.notification-message').textContent = text;
    bindCopy(notice.querySelector('.notification-copy'));
  }
  notice.append(close);
  root.append(notice);
  while (root.children.length > 5) root.firstElementChild.remove();
  let timer;
  let remaining = 5000;
  let started = Date.now();
  const dismiss = () => {
    clearTimeout(timer);
    if (!notice.isConnected || notice.classList.contains('leaving')) return;
    notice.classList.add('leaving');
    setTimeout(() => notice.remove(), 220);
  };
  close.onclick = dismiss;
  const resume = () => { started = Date.now(); timer = setTimeout(dismiss, remaining); };
  notice.onmouseenter = () => { clearTimeout(timer); remaining -= Date.now() - started; };
  notice.onmouseleave = () => { if (remaining <= 0) dismiss(); else resume(); };
  resume();
  return notice;
}

const notifyError = error => notify(error?.message || error, 'error');

async function api(path, options = {}) {
  options.headers = {...(options.headers || {})};
  if (options.body && !(options.body instanceof FormData)) {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(options.body);
  }
  if (csrf) options.headers['X-CSRF-Token'] = csrf;
  const response = await fetch(path, options);
  const text = await response.text();
  let value = {};
  if (text) {
    try { value = JSON.parse(text); }
    catch { value = {message: text.trim()}; }
  }
  if (!response.ok) {
    const message = typeof value === 'string'
      ? value
      : value?.error || value?.message || `HTTP ${response.status}`;
    const error = new Error(message);
    error.status = response.status;
    error.payload = value;
    throw error;
  }
  return value;
}

const fmtBytes = value => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let index = 0;
  let n = Number(value || 0);
  while (n >= 1024 && index < units.length - 1) { n /= 1024; index++; }
  return `${n.toFixed(index ? 1 : 0)} ${units[index]}`;
};
const fmtPercent = (used, total) => total ? `${(Number(used || 0) * 100 / Number(total)).toFixed(1)}%` : '-';
const fmtUptime = seconds => {
  let value = Math.max(0, Number(seconds || 0));
  const days = Math.floor(value / 86400);
  value %= 86400;
  const hours = Math.floor(value / 3600);
  const minutes = Math.floor(value % 3600 / 60);
  return `${days ? `${days} d ` : ''}${hours} h ${minutes} min`;
};
const escapeHTML = value => String(value ?? '').replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const buttonHTML = (label, variant = 'primary', attributes = '') => {
  const className = variant === 'primary' ? '' : ` class="button-${variant}"`;
  return `<button${className}${attributes ? ` ${attributes}` : ''}>${escapeHTML(label)}</button>`;
};
function setDialogAction(label, danger = false) {
  const button = document.querySelector('#dialog-ok');
  const cancel = document.querySelector('#dialog-form [value="cancel"]');
  if (cancel) cancel.hidden = false;
  button.textContent = label;
  button.className = danger ? 'button-danger' : '';
  return button;
}
function openDialog(dialog) {
  document.documentElement.classList.add('dialog-open');
  document.body.classList.add('dialog-open');
  dialog.showModal();
}
async function runPendingAction(key, control, pendingLabel, action) {
  if (pendingActions.has(key)) return false;
  pendingActions.add(key);
  const previousLabel = control?.textContent;
  if (control) {
    control.disabled = true;
    if (pendingLabel) control.textContent = pendingLabel;
  }
  try {
    await action();
    return true;
  } finally {
    pendingActions.delete(key);
    if (control?.isConnected) {
      control.disabled = false;
      if (pendingLabel) control.textContent = previousLabel;
    }
  }
}
const localInput = value => {
  const d = value ? new Date(value) : new Date(Date.now() + 30 * 86400000);
  const offset = d.getTimezoneOffset() * 60000;
  return new Date(d.getTime() - offset).toISOString().slice(0, 16);
};
function remaining(group) {
  if (group.unlimited) return 'Unlimited';
  if (!group.expires_at) return 'No expiration date';
  const ms = new Date(group.expires_at) - Date.now();
  if (ms <= 0) return 'Expired';
  const days = Math.floor(ms / 86400000);
  const hours = Math.floor((ms % 86400000) / 3600000);
  return `${days} d ${hours} h`;
}

async function boot() {
  const pendingUpdate = sessionStorage.getItem(pendingUpdateKey);
  if (pendingUpdate) {
    try {
      await waitForUpdatedPanel(pendingUpdate);
      return;
    } catch (error) {
      sessionStorage.removeItem(pendingUpdateKey);
      notifyError(error);
    }
  }
  try { await load({showLoadingScreen: true}); } catch (error) {
    if (error?.status !== 401) throw error;
    const bootstrap = await api('/api/bootstrap/status');
    if (!bootstrap.needs_bootstrap) {
      showAuth();
      return;
    }
    app.innerHTML = '<section class="center"><div class="card"><h1>Simple Bridge Panel</h1><p class="muted">Panel setup is incomplete.</p></div></section>';
    notify('The admin account was not created. Run bootstrap.sh again over SSH.', 'error', 'Setup required');
  }
}

async function waitForUpdatedPanel(target) {
  if (!document.querySelector('.update-progress')) showUpdateLoading(target);
  const deadline = Date.now() + 6 * 60 * 1000;
  let lastChange = Date.now();
  let lastProgress = -1;
  let lastStage = '';
  while (Date.now() < deadline) {
    try {
      const update = await api(`/api/update/progress?update-wait=${Date.now()}`, {cache: 'no-store'});
      if (update.target && update.target !== target) {
        const error = new Error(`Another update is running for v${update.target}.`);
        error.updateFatal = true;
        throw error;
      }
      if (update.status === 'error') {
        const error = new Error(update.error || 'The update failed.');
        error.updateFatal = true;
        throw error;
      }
      if (update.status !== 'idle') {
        const progress = Number(update.progress) || 0;
        const stage = update.stage || 'Installing update.';
        setUpdateProgress(progress, stage);
        if (progress !== lastProgress || stage !== lastStage) {
          lastProgress = progress;
          lastStage = stage;
          lastChange = Date.now();
        }
      }
      const current = await api(`/api/state?update-wait=${Date.now()}`, {cache: 'no-store'});
      if (current.version === target) {
        setUpdateProgress(100, 'Update installed. Reopening the panel.');
        sessionStorage.removeItem(pendingUpdateKey);
        sessionStorage.removeItem(updateCacheKey);
        await sleep(450);
        location.reload();
        return;
      }
    } catch (error) {
      if (error?.status === 401 || error?.updateFatal) throw error;
    }
    if (Date.now() - lastChange > 150000) {
      const error = new Error(`Update to v${target} stopped making progress. Refresh the dashboard and try again.`);
      error.updateFatal = true;
      throw error;
    }
    await sleep(900);
  }
  const error = new Error(`Update to v${target} did not finish within six minutes. Refresh the dashboard and try again.`);
  error.updateFatal = true;
  throw error;
}

function showAuth() {
  viewGeneration++;
  stopMetricsPolling();
  app.innerHTML = '';
  app.append(document.querySelector('#auth').content.cloneNode(true));
  const password = document.querySelector('#password');
  const loginForm = document.querySelector('#auth-form');
  const changeForm = document.querySelector('#password-change-form');
  const changeOpen = document.querySelector('#password-change-open');
  const showLogin = () => {
    loginForm.hidden = false;
    changeOpen.hidden = false;
    changeForm.hidden = true;
  };
  const showPasswordChange = () => {
    loginForm.hidden = true;
    changeOpen.hidden = true;
    changeForm.hidden = false;
    document.querySelector('#current-password').focus();
  };
  showLogin();
  loginForm.onsubmit = async event => {
    event.preventDefault();
    try {
      const v = await api('/api/login', {method: 'POST', body: {Login: 'admin', Password: password.value}});
      csrf = v.csrf;
      await load({showLoadingScreen: true});
    } catch (error) { notifyError(error); }
  };
  changeOpen.onclick = showPasswordChange;
  document.querySelector('#password-change-cancel').onclick = showLogin;
  changeForm.onsubmit = async event => {
    event.preventDefault();
    const current = document.querySelector('#current-password');
    const next = document.querySelector('#new-password');
    const confirmation = document.querySelector('#confirm-password');
    if (next.value !== confirmation.value) {
      notify('The new passwords do not match.', 'error');
      confirmation.focus();
      return;
    }
    try {
      const value = await api('/api/password', {method: 'POST', body: {CurrentPassword: current.value, NewPassword: next.value, ConfirmPassword: confirmation.value}});
      changeForm.reset();
      showLogin();
      password.value = '';
      password.focus();
      notify(value.message || 'Password changed.', 'success');
    } catch (error) { notifyError(error); }
  };
}

const settled = promise => promise.then(value => ({value}), error => ({error}));

function indexDevices(devices = [], groups = []) {
  const indexed = new Map((groups || []).map(group => [Number(group.id), {value: {devices: []}}]));
  for (const device of devices || []) {
    const groupID = Number(device.group_id);
    if (!indexed.has(groupID)) indexed.set(groupID, {value: {devices: []}});
    indexed.get(groupID).value.devices.push(device);
  }
  return indexed;
}

async function load({showLoadingScreen = false} = {}) {
  const generation = ++viewGeneration;
  const loadingStarted = performance.now();
  if (showLoadingScreen) showLoading();
  const stateRequest = api('/api/state');
  const discoveryRequest = settled(api('/api/discovery'));
  const metricsRequest = settled(api('/api/metrics'));
  const roomsRequest = settled(api('/api/bypass/rooms'));
  const nextState = await stateRequest;
  const [discovery, metrics, rooms] = await Promise.all([
    discoveryRequest,
    metricsRequest,
    roomsRequest
  ]);
  if (showLoadingScreen) {
    await dashboardVisualsReady;
    const remaining = minimumLoadingMilliseconds - (performance.now() - loadingStarted);
    if (remaining > 0) await sleep(remaining);
  }
  if (generation !== viewGeneration) return false;
  csrf = nextState.csrf;
  state = nextState;
  const scrollPosition = showLoadingScreen ? null : {left: window.scrollX, top: window.scrollY};
  render({discovery, metrics, rooms, devices: indexDevices(nextState.devices, nextState.groups)});
  if (scrollPosition) requestAnimationFrame(() => window.scrollTo(scrollPosition));
  return true;
}

async function refreshGroups() {
  if (!document.querySelector('#groups')) return false;
  const generation = ++viewGeneration;
  const nextState = await api('/api/state');
  if (generation !== viewGeneration || !document.querySelector('#groups')) return false;
  csrf = nextState.csrf;
  state = nextState;
  setupServerLink();
  renderGroups(indexDevices(nextState.devices, nextState.groups));
  if (lastMetrics) applyGroupMetrics(lastMetrics);
  return true;
}

function render(initial = {}) {
  stopMetricsPolling();
  activeLifecycle = initial.discovery?.value?.lifecycle?.status === 'running' ? initial.discovery.value.lifecycle : null;
  app.innerHTML = '';
  app.append(document.querySelector('#dashboard').content.cloneNode(true));
  document.querySelector('#refresh').onclick = async event => {
    try {
      await runPendingAction('dashboard:refresh', event.currentTarget, 'Refreshing…', async () => {
        if (await load()) notify('Dashboard refreshed.', 'success');
      });
    }
    catch (e) { notifyError(e); }
  };
  setupUpdater();
  document.querySelector('#logout').onclick = async () => {
    try {
      await api('/api/logout', {method: 'POST'});
      csrf = '';
      state = null;
      showAuth();
      notify('Session closed.', 'success', 'Signed out');
    } catch (e) { notifyError(e); }
  };
  document.querySelector('#new-group').onclick = () => groupDialog();
  setupServerLink();
  renderGroups(initial.devices);
  loadDiscovery(initial.discovery);
  loadMetrics(false, initial.metrics);
  loadBypassRooms(initial.rooms);
  startMetricsPolling();
}

function setupUpdater() {
  const button = document.querySelector('#update-check');
  const prereleases = document.querySelector('#update-prereleases');
  if (!button || !prereleases) return;
  let requestGeneration = 0;
  const clearCache = () => {
    try { sessionStorage.removeItem(updateCacheKey); } catch {}
  };
  const restoreCache = () => {
    try {
      const cached = JSON.parse(sessionStorage.getItem(updateCacheKey) || 'null');
      const checkedAt = Number(cached?.checked_at);
      const info = cached?.info;
      const age = Date.now() - checkedAt;
      const validVersion = !info?.update_available || /^\d+\.\d+\.\d+$/.test(info.latest_version || '');
      if (!info || typeof info.update_available !== 'boolean' || info.include_prereleases !== prereleases.checked || info.current_version !== state?.version || !Number.isFinite(checkedAt) || age < 0 || age >= updateCacheMilliseconds || !validVersion) throw new Error('stale update cache');
      updateInfo = info;
      updateCheckedAt = checkedAt;
      return true;
    } catch {
      clearCache();
      return false;
    }
  };
  const renderButton = () => {
    if (updateInfo?.update_available) {
      button.textContent = `Update to v${updateInfo.latest_version}`;
      button.classList.remove('button-secondary');
    } else {
      button.textContent = 'Check for updates';
      button.classList.add('button-secondary');
    }
    button.disabled = Boolean(activeLifecycle);
    prereleases.disabled = Boolean(activeLifecycle);
  };
  const check = async (announce = true) => {
    const generation = ++requestGeneration;
    const includePrereleases = prereleases.checked;
    button.disabled = true;
    prereleases.disabled = true;
    button.textContent = 'Checking…';
    try {
      const info = await api(`/api/update${includePrereleases ? '?include_prereleases=1' : ''}`);
      if (generation !== requestGeneration || !button.isConnected || prereleases.checked !== includePrereleases) return;
      updateInfo = info;
      updateCheckedAt = Date.now();
      try { sessionStorage.setItem(updateCacheKey, JSON.stringify({checked_at: updateCheckedAt, info: updateInfo})); } catch {}
      renderButton();
      if (announce) {
        notify(updateInfo.update_available
          ? `Simple Bridge Panel v${updateInfo.latest_version} is available. Click again to install.`
          : (updateInfo.message || 'The latest available version is installed.'),
        updateInfo.update_available ? 'warning' : 'success', 'Updates');
      }
    } catch (e) {
      if (generation !== requestGeneration || !button.isConnected) return;
      updateInfo = null;
      updateCheckedAt = Date.now();
      clearCache();
      renderButton();
      if (announce) notifyError(e);
    }
  };
  prereleases.onchange = () => {
    requestGeneration++;
    updateInfo = null;
    updateCheckedAt = 0;
    clearCache();
    check(true);
  };
  button.onclick = async () => {
    if (!updateInfo?.update_available) {
      await check(true);
      return;
    }
    const target = updateInfo.latest_version;
    sessionStorage.setItem(pendingUpdateKey, target);
    clearCache();
    showUpdateLoading(target);
    try {
      const updatePath = `/api/update${updateInfo.include_prereleases ? '?include_prereleases=1' : ''}`;
      const progress = await api(updatePath, {method: 'POST'});
      setUpdateProgress(progress.progress, progress.stage);
      await waitForUpdatedPanel(target);
    } catch (e) {
      // A dropped connection can mean that systemd restarted the panel before
      // the response reached the browser. Reload and let boot() verify it.
      if (!e?.status && !e?.updateFatal) {
        location.reload();
        return;
      }
      sessionStorage.removeItem(pendingUpdateKey);
      await load();
      notifyError(e);
    }
  };
  if ((!updateInfo || updateInfo.current_version !== state?.version || updateInfo.include_prereleases !== prereleases.checked || Date.now() - updateCheckedAt >= updateCacheMilliseconds) && !restoreCache()) check(false);
  else renderButton();
}

function setupServerLink() {
  const link = document.querySelector('#server-link');
  const edit = document.querySelector('#server-link-edit');
  if (!link || !edit || !state) return;
  if (state.server_url) {
    link.href = state.server_url;
    try { link.textContent = new URL(state.server_url).hostname; } catch { link.textContent = 'Server page'; }
    link.hidden = false;
    edit.textContent = 'Edit';
  } else {
    link.hidden = true;
    edit.textContent = 'Add server page';
  }
  edit.onclick = () => {
    const generation = ++dialogGeneration;
    const dialog = document.querySelector('#dialog');
    const body = document.querySelector('#dialog-body');
    document.querySelector('#dialog-title').textContent = 'Server page';
    setDialogAction('Save');
    body.innerHTML = `<label>URL<input id="server-url" type="url" placeholder="https://hosting.example/server/123" value="${escapeHTML(state.server_url || '')}"></label><small class="muted">A plain text link to the hosting dashboard or this server page will appear in the header.</small>`;
    openDialog(dialog);
    document.querySelector('#dialog-form').onsubmit = async event => {
      if (event.submitter?.value === 'cancel') return;
      event.preventDefault();
      const submit = event.submitter || document.querySelector('#dialog-ok');
      try {
        await runPendingAction('settings:server-url', submit, 'Saving…', async () => {
          const value = await api('/api/settings/server-url', {method: 'PUT', body: {URL: body.querySelector('#server-url').value}});
          if (generation === dialogGeneration && dialog.open) dialog.close();
          state.server_url = value.server_url;
          setupServerLink();
          notify('Server page URL saved.', 'success');
        });
      } catch (e) { notifyError(e); }
    };
  };
}

function groupDialog(group = null) {
  const generation = ++dialogGeneration;
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  document.querySelector('#dialog-title').textContent = group ? 'Group settings' : 'New group';
  setDialogAction(group ? 'Save' : 'Create');
  body.innerHTML = `
    <label>Name<input id="group-name" value="${escapeHTML(group?.name || 'My group')}" maxlength="160" required><small class="muted">Letters, numbers, spaces, and hyphens only. Spaces become underscores in the public link.</small></label>
    <label>Contact<input id="group-contact" value="${escapeHTML(group?.contact || '')}" placeholder="Telegram, phone number, name - any text"></label>
    <label class="toggle option"><input type="checkbox" id="group-unlimited" ${group?.unlimited ? 'checked' : ''}><span>Unlimited group</span></label>
    <label id="expiry-row">Expires<input id="group-expiry" type="datetime-local" value="${localInput(group?.expires_at)}"></label>`;
  const unlimited = body.querySelector('#group-unlimited');
  const expiryRow = body.querySelector('#expiry-row');
  const expiry = body.querySelector('#group-expiry');
  const sync = () => {
    expiryRow.hidden = unlimited.checked;
    expiry.required = !unlimited.checked;
  };
  unlimited.onchange = sync;
  sync();
  openDialog(dialog);
  document.querySelector('#dialog-form').onsubmit = async event => {
    if (event.submitter?.value === 'cancel') return;
    event.preventDefault();
    const submit = event.submitter || document.querySelector('#dialog-ok');
    const actionKey = group ? `group:${group.id}:update` : 'group:create';
    try {
      await runPendingAction(actionKey, submit, group ? 'Saving…' : 'Creating…', async () => {
        const payload = {
          Name: body.querySelector('#group-name').value,
          Contact: body.querySelector('#group-contact').value,
          Unlimited: unlimited.checked,
          ExpiresAt: unlimited.checked ? '' : new Date(expiry.value).toISOString(),
          Days: 30
        };
        if (group) await api(`/api/groups/${group.id}`, {method: 'PUT', body: payload});
        else await api('/api/groups', {method: 'POST', body: payload});
        if (generation === dialogGeneration && dialog.open) dialog.close();
        await refreshGroups();
        if (group) await loadBypassRooms();
        notify(group ? 'Group updated.' : 'Group created.', 'success');
      });
    } catch (e) { notifyError(e); }
  };
}

function renderGroups(prefetchedDevices = null) {
  const root = document.querySelector('#groups');
  if (!root) return;
  if (!state.groups?.length) {
    root.innerHTML = '<div class="card muted">No groups yet. Create the first one.</div>';
    return;
  }
  root.querySelector('.card.muted')?.remove();
  const existing = new Map(Array.from(root.querySelectorAll('.group-panel[data-group-id]')).map(card => [Number(card.dataset.groupId), card]));
  const active = new Set();
  for (const group of state.groups) {
    const groupID = Number(group.id);
    active.add(groupID);
    let card = existing.get(groupID);
    if (!card) {
      card = document.createElement('article');
      card.className = 'group-panel';
      card.dataset.groupId = String(groupID);
      card.innerHTML = `
        <div class="group-summary">
          <div class="group-title"><h3 data-group-name></h3><span class="group-contact" data-group-contact></span></div>
          <div><span class="group-stat-label">Expires</span><span data-group-expiry></span></div>
          <div><span class="group-stat-label">Monthly traffic</span><span class="traffic" data-group-traffic></span><small class="muted" data-group-traffic-details></small></div>
          <div class="group-actions">${buttonHTML('+1 month', 'primary', 'data-extend')}${buttonHTML('+Device', 'secondary', 'data-device')}${buttonHTML('Copy check link', 'secondary', 'data-check-link')}${buttonHTML('Edit', 'secondary', 'data-edit')}${buttonHTML('Remove', 'danger', 'data-delete')}</div>
        </div>
        <div class="devices-wrap"><table class="devices-table"><thead><tr><th>Device</th><th>Method</th><th>Monthly traffic</th><th>Status</th><th>Actions</th></tr></thead><tbody class="devices"><tr data-empty><td colspan="5" class="empty-row">Loading devices…</td></tr></tbody></table></div>`;
    }
    const expiry = group.unlimited ? 'Unlimited' : group.expires_at ? new Date(group.expires_at).toLocaleString('en-GB') : 'Not set';
    const expiryContent = group.unlimited
      ? '<b class="expiry-value unlimited">Unlimited</b>'
      : group.expires_at
        ? `<span class="expiry-value">${escapeHTML(expiry)}</span><b class="countdown ${group.status === 'active' ? '' : 'expired'}">${remaining(group)}</b>`
        : `<span class="expiry-value">${escapeHTML(expiry)}</span>`;
    card.querySelector('[data-group-name]').textContent = group.name;
    const contact = card.querySelector('[data-group-contact]');
    contact.textContent = group.contact || '';
    contact.hidden = !group.contact;
    card.querySelector('[data-group-expiry]').innerHTML = expiryContent;
    const traffic = card.querySelector('[data-group-traffic]');
    traffic.dataset.groupTraffic = String(groupID);
    traffic.textContent = fmtBytes((group.rx_bytes || 0) + (group.tx_bytes || 0));
    const trafficDetails = card.querySelector('[data-group-traffic-details]');
    trafficDetails.dataset.groupTrafficDetails = String(groupID);
    card.querySelector('[data-extend]').hidden = Boolean(group.unlimited);
    card.querySelector('[data-edit]').onclick = () => groupDialog(group);
    card.querySelector('[data-extend]').onclick = async event => {
      try {
        await runPendingAction(`group:${group.id}:extend`, event.currentTarget, 'Extending…', async () => {
          await api(`/api/groups/${group.id}/extend`, {method: 'POST'});
          await refreshGroups();
          notify(`Group “${group.name}” extended by one month.`, 'success');
        });
      } catch (e) { notifyError(e); }
    };
    card.querySelector('[data-device]').onclick = () => deviceDialog({group_id: group.id});
    card.querySelector('[data-check-link]').onclick = async () => {
      try {
        const slug = group.name.trim().replace(/\s+/g, '_');
        const checkURL = `${window.location.origin}/check/${slug}`;
        await copyText(checkURL);
        notify(checkURL, 'success', `Check link copied · ${group.name}`);
      } catch (e) { notifyError(e); }
    };
    card.querySelector('[data-delete]').onclick = async event => {
      if (!confirm(`Remove group “${group.name}” and all its devices? Active credentials will be revoked.`)) return;
      try {
        await runPendingAction(`group:${group.id}:delete`, event.currentTarget, 'Removing…', async () => {
          const removed = await api(`/api/groups/${group.id}`, {method: 'DELETE'});
          await refreshGroups();
          await loadBypassRooms();
          notify(removed.warning || `Group “${group.name}” removed.`, removed.warning ? 'warning' : 'success');
        });
      } catch (e) { notifyError(e); }
    };
    root.append(card);
    renderDevices(card.querySelector('.devices'), prefetchedDevices?.get(groupID)?.value?.devices || []);
  }
  for (const [groupID, card] of existing) {
    if (!active.has(groupID)) card.remove();
  }
}

function deviceDialog(device) {
  const generation = ++dialogGeneration;
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  const editing = Boolean(device.id);
  document.querySelector('#dialog-title').textContent = editing ? 'Device' : 'New device';
  setDialogAction(editing ? 'Save' : 'Create');
  const selectedMethod = device.method === 'amneziawg'
    ? `amneziawg-${device.format === 'app' ? 'app' : 'native'}`
    : (device.method || 'xray');
  body.innerHTML = `
    <label>Name<input id="device-name" value="${escapeHTML(device.name || 'Phone')}" required></label>
    <label>Protocol<select id="device-method" ${editing ? 'disabled' : ''}>${DEVICE_METHOD_OPTIONS.map(([id, label]) => `<option value="${id}" ${id === selectedMethod ? 'selected' : ''}>${label}</option>`).join('')}</select></label>
    ${editing ? '<small class="muted">Protocol and format are bound to the credential. Create another device to use a different option.</small>' : ''}`;
  openDialog(dialog);
  document.querySelector('#dialog-form').onsubmit = async event => {
    if (event.submitter?.value === 'cancel') return;
    event.preventDefault();
    const submit = event.submitter || document.querySelector('#dialog-ok');
    const actionKey = editing ? `device:${device.id}:update` : `group:${device.group_id}:device:create`;
    try {
      await runPendingAction(actionKey, submit, editing ? 'Saving…' : 'Creating…', async () => {
        const payload = {Name: body.querySelector('#device-name').value, Method: body.querySelector('#device-method').value};
        if (editing) {
          await api(`/api/devices/${device.id}`, {method: 'PUT', body: {Name: payload.Name}});
          if (generation === dialogGeneration && dialog.open) dialog.close();
          await refreshGroups();
          notify('Device updated.', 'success');
        } else {
          const created = await api(`/api/groups/${device.group_id}/devices`, {method: 'POST', body: payload});
          if (generation === dialogGeneration && dialog.open) dialog.close();
          await refreshGroups();
          if (payload.Method.startsWith('bypass-')) await loadBypassRooms();
          await copyCredential(payload.Name, created.credential, created.id);
        }
      });
    } catch (e) {
      if (!editing && e?.status === 409) {
        try { await refreshGroups(); } catch {}
      }
      notifyError(e);
    }
  };
}

function syncDeviceToggle(job) {
  document.querySelectorAll(`[data-device-toggle="${job.id}"]`).forEach(checkbox => {
    checkbox.checked = job.desired;
    checkbox.setAttribute('aria-label', `${job.desired ? 'Disable' : 'Enable'} ${job.name}`);
  });
}

async function flushDeviceToggle(job) {
  if (job.running) return;
  job.running = true;
  try {
    while (job.confirmed !== job.desired) {
      const target = job.desired;
      try {
        await api(`/api/devices/${job.id}/enabled`, {method: 'PUT', body: {Enabled: target}});
        job.confirmed = target;
        job.device.enabled = target;
      } catch (error) {
        job.desired = job.confirmed;
        syncDeviceToggle(job);
        notifyError(error);
        break;
      }
    }
  } finally {
    job.running = false;
    if (job.confirmed === job.desired) deviceToggleJobs.delete(job.id);
  }
}

function queueDeviceToggle(device, desired) {
  const id = Number(device.id);
  let job = deviceToggleJobs.get(id);
  if (!job) {
    job = {id, name: device.name, device, confirmed: Boolean(device.enabled), desired: Boolean(device.enabled), running: false};
    deviceToggleJobs.set(id, job);
  }
  job.name = device.name;
  job.device = device;
  job.desired = Boolean(desired);
  syncDeviceToggle(job);
  void flushDeviceToggle(job);
}

function renderDevices(root, devices = []) {
  if (!devices.length) {
    root.querySelectorAll('tr[data-device-id]').forEach(row => row.remove());
    let empty = root.querySelector('tr[data-empty]');
    if (!empty) {
      empty = document.createElement('tr');
      empty.dataset.empty = '';
      empty.innerHTML = '<td colspan="5" class="empty-row"></td>';
      root.append(empty);
    }
    empty.querySelector('td').textContent = 'No devices';
    return;
  }
  root.querySelector('tr[data-empty]')?.remove();
  const existing = new Map(Array.from(root.querySelectorAll('tr[data-device-id]')).map(row => [Number(row.dataset.deviceId), row]));
  const active = new Set();
  for (const device of devices) {
    const deviceID = Number(device.id);
    active.add(deviceID);
    let row = existing.get(deviceID);
    if (!row) {
      row = document.createElement('tr');
      row.dataset.deviceId = String(deviceID);
      row.innerHTML = `<td><b data-device-name></b></td><td><span class="method" data-device-method></span></td><td data-device-traffic></td><td data-device-status></td><td><div class="device-actions">${buttonHTML('Copy', 'secondary', 'data-key')}${buttonHTML('Edit', 'secondary', 'data-edit')}${buttonHTML('Remove', 'danger', 'data-delete')}</div></td>`;
    }
    const revocable = device.method === 'xray' || device.method === 'xray-xhttp' || device.method === 'amneziawg';
    const toggleJob = deviceToggleJobs.get(deviceID);
    if (toggleJob) {
      toggleJob.device = device;
      toggleJob.name = device.name;
    }
    const displayedEnabled = toggleJob ? toggleJob.desired : Boolean(device.enabled);
    row.querySelector('[data-device-name]').textContent = device.name;
    row.querySelector('[data-device-method]').textContent = deviceMethodLabel(device);
    const trafficCell = row.querySelector('[data-device-traffic]');
    trafficCell.dataset.deviceTraffic = String(deviceID);
    trafficCell.textContent = revocable ? fmtBytes((device.rx_bytes || 0) + (device.tx_bytes || 0)) : '-';
    const status = row.querySelector('[data-device-status]');
    let checkbox = status.querySelector('input');
    if (revocable) {
      if (!checkbox) {
        status.innerHTML = '<label class="toggle"><input type="checkbox"></label>';
        checkbox = status.querySelector('input');
      }
      checkbox.dataset.deviceToggle = String(deviceID);
      checkbox.checked = displayedEnabled;
      checkbox.setAttribute('aria-label', `${displayedEnabled ? 'Disable' : 'Enable'} ${device.name}`);
      checkbox.onchange = () => queueDeviceToggle(device, checkbox.checked);
    } else if (checkbox) {
      status.replaceChildren();
    }
    row.querySelector('[data-key]').onclick = event => {
      runPendingAction(`device:${device.id}:copy`, event.currentTarget, 'Copying…', () =>
        copyCredential(device.name, device.credential, device.id)
      ).catch(notifyError);
    };
    row.querySelector('[data-edit]').onclick = () => deviceDialog(device);
    row.querySelector('[data-delete]').onclick = async event => {
      if (!confirm(`Remove device “${device.name}”? Its credentials will also be deleted.`)) return;
      try {
        await runPendingAction(`device:${device.id}:delete`, event.currentTarget, 'Removing…', async () => {
          const removed = await api(`/api/devices/${device.id}`, {method: 'DELETE'});
          await refreshGroups();
          if (device.method.startsWith('bypass-')) await loadBypassRooms();
          notify(removed.warning || `Device “${device.name}” removed.`, removed.warning ? 'warning' : 'success');
        });
      } catch (e) { notifyError(e); }
    };
    root.append(row);
  }
  for (const [deviceID, row] of existing) {
    if (!active.has(deviceID)) row.remove();
  }
}

async function copyCredential(title, credential, id) {
  if (!credential && id) {
    const value = await api(`/api/devices/${id}/credential`);
    credential = value.credential;
  }
  if (!credential) {
    notify('Credential not found. This device was created by an older panel version.', 'error');
    return;
  }
  await copyText(credential);
  notify(credential, 'success', `Credential copied · ${title}`, {
    qr: `/api/devices/${id}/qr`,
    qrFilename: `${title || 'credential'}-qr.png`
  });
}

async function runComponentLifecycle(component, button, operation) {
  const installing = operation === 'install';
  const path = operation === 'external-remove'
    ? `/api/components/${component.id}/external`
    : installing ? `/api/components/${component.id}/install` : `/api/components/${component.id}`;
  try {
    return await runPendingAction(
      `component:${component.id}:lifecycle`,
      button,
      installing ? 'Installing…' : 'Removing…',
      async () => {
        await api(path, {method: installing ? 'POST' : 'DELETE'});
        setLifecycleControls({component_id: component.id, operation, status: 'running'});
        await watchJob(component.id, button, installing ? 'install' : 'uninstall', {throwOnError: true});
      }
    );
  } catch (error) {
    setLifecycleControls(null);
    if (button.isConnected) { button.disabled = false; button.textContent = 'Retry'; }
    notifyError(error);
    return false;
  }
}

function lifecyclePendingLabel(operation) {
  if (operation === 'install') return 'Installing…';
  if (operation === 'external-remove') return 'Removing…';
  return 'Removing…';
}

function setLifecycleControls(job) {
  activeLifecycle = job?.status === 'running' ? job : null;
  const blocked = Boolean(activeLifecycle);
  const controls = [
    ...document.querySelectorAll('.component-actions button'),
    document.querySelector('#update-check'),
    document.querySelector('#update-prereleases')
  ].filter(Boolean);
  for (const control of controls) {
    if (blocked) {
      if (control.dataset.lifecycleWasDisabled === undefined) control.dataset.lifecycleWasDisabled = control.disabled ? '1' : '0';
      control.disabled = true;
      continue;
    }
    if (control.dataset.lifecycleWasDisabled !== undefined) {
      control.disabled = control.dataset.lifecycleWasDisabled === '1';
      delete control.dataset.lifecycleWasDisabled;
    }
  }
}

function resumeLifecycle(job) {
  const running = job?.status === 'running' && job.component_id;
  const generation = ++lifecycleWatchGeneration;
  setLifecycleControls(running ? job : null);
  if (!running) return;
  const button = document.querySelector('[data-lifecycle-active]');
  watchJob(job.component_id, button, job.operation || 'operation', {
    throwOnError: true,
    onDone: async completed => {
      if (generation !== lifecycleWatchGeneration) return;
      await load();
      notify(completed.output || 'Component operation completed.', 'success');
    }
  }).catch(async error => {
    if (generation !== lifecycleWatchGeneration) return;
    setLifecycleControls(null);
    await loadDiscovery();
    notifyError(error);
  });
}

function externalRemovalDialog(component) {
  const generation = ++dialogGeneration;
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  document.querySelector('#dialog-title').textContent = `Remove external ${component.name}?`;
  setDialogAction('Remove external', true);
  const warning = component.id === 'docker'
    ? 'SBP will remove an external Ubuntu docker.io package only if containers, images, volumes, and custom networks are all absent. External configuration and data directories are never deleted.'
    : 'SBP will remove exact BBR and fq assignments from detected sysctl configuration files, preserve every other setting, and then reset the active kernel values.';
  body.innerHTML = `<p>${escapeHTML(warning)}</p><p class="muted">After removal, install the component again to make it fully managed by SBP.</p>`;
  openDialog(dialog);
  document.querySelector('#dialog-form').onsubmit = async event => {
    if (event.submitter?.value === 'cancel') return;
    event.preventDefault();
    const submit = event.submitter || document.querySelector('#dialog-ok');
    const removed = await runComponentLifecycle(component, submit, 'external-remove');
    if (removed && generation === dialogGeneration && dialog.open) dialog.close();
  };
}

function bypassSettingsDialog(component) {
  const settings = BYPASS_COMPONENT_SETTINGS[component.id];
  if (!settings) return;
  const generation = ++dialogGeneration;
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  const form = document.querySelector('#dialog-form');
  document.querySelector('#dialog-title').textContent = `${settings.label} settings`;
  setDialogAction('Close');
  const cancel = form.querySelector('[value="cancel"]');
  if (cancel) cancel.hidden = true;
  body.innerHTML = '<p class="muted">Loading provider settings…</p>';
  form.onsubmit = () => {};
  openDialog(dialog);

  const render = () => {
    if (generation !== dialogGeneration || !dialog.open) return;
    body.innerHTML = `
      <p class="settings-notice">${escapeHTML(GLOBAL_COMPONENT_SETTINGS_NOTICE)}</p>
      <p class="muted">Upload or replace the account cookie JSON used by ${escapeHTML(settings.label)}. It is stored in a root-only server directory and can be prepared before the component is installed.</p>
      <label class="drop bypass-settings-drop">Drop the cookie JSON file here<input type="file" accept="application/json,.json"></label>
      <div class="saved-rooms"><span class="saved-rooms-title">Rooms</span><div class="saved-room-list" data-bypass-rooms></div></div>
      <div class="upload-footer"><button type="button" class="button-danger" data-clear-bypass>Clear credentials</button></div>`;
    const input = body.querySelector('input[type="file"]');
    const drop = body.querySelector('.bypass-settings-drop');
    const rooms = body.querySelector('[data-bypass-rooms]');
    renderBypassRooms(rooms, settings.provider);

    const upload = async file => {
      if (!file) return;
      const payload = new FormData();
      payload.append('cookies', file);
      try {
        await runPendingAction(`bypass:${settings.provider}:upload`, input, '', async () => {
          const value = await api(`/api/bypass/${settings.provider}/credentials`, {method: 'POST', body: payload});
          input.value = '';
          notify(`Uploaded: ${value.filename}, ${value.bytes} bytes`, 'success', `${settings.label} cookies uploaded`);
        });
      } catch (error) { notifyError(error); }
    };
    input.onchange = () => upload(input.files[0]);
    for (const name of ['dragenter', 'dragover']) drop.addEventListener(name, event => {
      event.preventDefault();
      drop.classList.add('drag');
    });
    for (const name of ['dragleave', 'drop']) drop.addEventListener(name, event => {
      event.preventDefault();
      drop.classList.remove('drag');
    });
    drop.addEventListener('drop', event => upload(event.dataTransfer.files[0]));
    body.querySelector('[data-clear-bypass]').onclick = async event => {
      if (!confirm(`Clear cookies and stop the current “${settings.label}” session? Devices and saved group rooms will remain. Upload a new JSON file to start them again.`)) return;
      try {
        await runPendingAction(`bypass:${settings.provider}:clear`, event.currentTarget, 'Clearing…', async () => {
          const value = await api(`/api/bypass/${settings.provider}/credentials`, {method: 'DELETE'});
          input.value = '';
          notify(value.message || 'Credentials cleared.', 'success');
        });
      } catch (error) { notifyError(error); }
    };
  };

  api('/api/bypass/rooms')
    .then(value => {
      bypassRooms = Array.isArray(value?.rooms) ? value.rooms : [];
      render();
    })
    .catch(error => {
      if (generation === dialogGeneration && dialog.open) dialog.close();
      notifyError(error);
    });
}

function xrayRealitySNIDialog(component) {
  const generation = ++dialogGeneration;
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  const form = document.querySelector('#dialog-form');
  document.querySelector('#dialog-title').textContent = `${component.name} settings`;
  setDialogAction('Add SNI');
  body.innerHTML = '<p class="muted">Loading REALITY SNI settings…</p>';
  openDialog(dialog);

  const renderSettings = settings => {
    if (generation !== dialogGeneration || !dialog.open) return;
    const defaultSNI = String(settings?.default_sni || '');
    const target = String(settings?.target || '');
    const names = Array.isArray(settings?.server_names) ? settings.server_names : [];
    const list = names.map(name => {
      const isDefault = name === defaultSNI;
      return `<li><span><code>${escapeHTML(name)}</code>${isDefault ? '<small>Default</small>' : ''}</span>${isDefault ? '' : buttonHTML('Remove', 'danger', `type="button" data-remove-sni="${escapeHTML(name)}"`)}</li>`;
    }).join('');
    body.innerHTML = `
      <p class="settings-notice">${escapeHTML(GLOBAL_COMPONENT_SETTINGS_NOTICE)}</p>
      <p class="muted">The default SNI remains in all generated profiles. Additional values become valid server-side choices for profiles edited manually in the client.</p>
      <label>REALITY target<div class="settings-inline-control"><input data-reality-target type="text" maxlength="259" value="${escapeHTML(target)}" placeholder="www.googletagmanager.com:443" autocomplete="off"><button type="button" class="button-secondary" data-save-reality-target>Save target</button></div><small class="settings-warning">The target must be a TLS hostname on port 443. SBP probes it before applying it to an installed component, or during a later installation. Already imported profiles are not rewritten.</small></label>
      <ul class="sni-list">${list}</ul>
      <label>Add SNI<input id="xray-reality-sni" type="text" maxlength="253" placeholder="dl.google.com" autocomplete="off" required><small class="muted">Enter a hostname only. If installed, the selected Xray container briefly reconnects after a validated change. Otherwise the value is saved for installation.</small></label>`;
    for (const remove of body.querySelectorAll('[data-remove-sni]')) {
      remove.onclick = async () => {
        const sni = remove.dataset.removeSni;
        if (!confirm(`Remove “${sni}” from the allowed SNI list? Profiles manually using it will stop connecting.`)) return;
        try {
          await runPendingAction(`component:${component.id}:sni`, remove, 'Removing…', async () => {
            const result = await api(`/api/components/${component.id}/reality-sni`, {method: 'DELETE', body: {sni}});
            renderSettings(result.settings);
            notify(`SNI “${sni}” removed.`, 'success');
          });
        } catch (error) { notifyError(error); }
      };
    }
    body.querySelector('[data-save-reality-target]').onclick = async event => {
      const input = body.querySelector('[data-reality-target]');
      const value = input?.value.trim();
      if (!value) return input?.focus();
      if (!confirm(`Change the REALITY target for “${component.name}” to “${value}”? Existing client profiles will not be rewritten.`)) return;
      try {
        await runPendingAction(`component:${component.id}:target`, event.currentTarget, 'Checking…', async () => {
          const result = await api(`/api/components/${component.id}/reality-sni`, {method: 'PUT', body: {target: value}});
          renderSettings(result.settings);
          notify(`REALITY target changed to “${result.settings.target}”.`, 'success');
        });
      } catch (error) { notifyError(error); }
    };
  };

  form.onsubmit = async event => {
    if (event.submitter?.value === 'cancel') return;
    event.preventDefault();
    const input = body.querySelector('#xray-reality-sni');
    if (!input?.reportValidity()) return;
    const submit = event.submitter || document.querySelector('#dialog-ok');
    const sni = input.value.trim();
    try {
      await runPendingAction(`component:${component.id}:sni`, submit, 'Adding…', async () => {
        const result = await api(`/api/components/${component.id}/reality-sni`, {method: 'POST', body: {sni}});
        renderSettings(result.settings);
        notify(`SNI “${sni}” is allowed for ${component.name}.`, 'success');
      });
    } catch (error) { notifyError(error); }
  };

  api(`/api/components/${component.id}/reality-sni`)
    .then(result => renderSettings(result.settings))
    .catch(error => {
      if (generation === dialogGeneration && dialog.open) dialog.close();
      notifyError(error);
    });
}

function componentTextSettingsDialog(component) {
  const generation = ++dialogGeneration;
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  const form = document.querySelector('#dialog-form');
  document.querySelector('#dialog-title').textContent = `${component.name} settings`;
  setDialogAction('Save');
  body.innerHTML = '<p class="muted">Loading server settings…</p>';
  openDialog(dialog);

  const render = settings => {
    if (generation !== dialogGeneration || !dialog.open) return;
    const warning = settings?.warning ? `<p class="settings-warning">${escapeHTML(settings.warning)}</p>` : '';
    const label = component.id === 'tweaks' ? 'Validated server commands' : 'AmneziaWG server parameters';
    body.innerHTML = `
      <p class="settings-notice">${escapeHTML(settings?.notice || GLOBAL_COMPONENT_SETTINGS_NOTICE)}</p>
      ${warning}
      <label>${escapeHTML(label)}<textarea class="component-settings-editor" rows="13" spellcheck="false" autocomplete="off">${escapeHTML(settings?.content || '')}</textarea><small class="muted">Only the displayed server-side keys are accepted. Missing lines are restored to validated defaults when saved.</small></label>
      <div class="settings-editor-actions"><button type="button" class="button-secondary" data-restore-component-defaults>Restore defaults</button></div>`;
    body.querySelector('[data-restore-component-defaults]').onclick = () => {
      body.querySelector('.component-settings-editor').value = String(settings?.default_content || '');
    };
  };

  form.onsubmit = async event => {
    if (event.submitter?.value === 'cancel') return;
    event.preventDefault();
    const editor = body.querySelector('.component-settings-editor');
    if (!editor) return;
    const submit = event.submitter || document.querySelector('#dialog-ok');
    try {
      await runPendingAction(`component:${component.id}:settings`, submit, 'Saving…', async () => {
        const result = await api(`/api/components/${component.id}/settings`, {method: 'PUT', body: {content: editor.value}});
        render(result.settings);
        const message = !component.installed
          ? `${component.name} settings saved for installation.`
          : component.id === 'tweaks' ? `${component.name} settings saved and reapplied.` : `${component.name} settings saved.`;
        notify(message, 'success');
      });
    } catch (error) { notifyError(error); }
  };

  api(`/api/components/${component.id}/settings`)
    .then(result => render(result.settings))
    .catch(error => {
      if (generation === dialogGeneration && dialog.open) dialog.close();
      notifyError(error);
    });
}

function readOnlyComponentSettingsDialog(component) {
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  const form = document.querySelector('#dialog-form');
  ++dialogGeneration;
  document.querySelector('#dialog-title').textContent = `${component.name} settings`;
  setDialogAction('Close');
  const cancel = form.querySelector('[value="cancel"]');
  if (cancel) cancel.hidden = true;
  body.innerHTML = `<p class="settings-notice">${escapeHTML(GLOBAL_COMPONENT_SETTINGS_NOTICE)}</p><p class="muted">This component has no editable server parameters. Its lifecycle and ownership policy are managed automatically by SBP.</p>`;
  form.onsubmit = () => {};
  openDialog(dialog);
}

function dockerSettingsDialog(component, containers) {
  const dialog = document.querySelector('#dialog');
  const body = document.querySelector('#dialog-body');
  const form = document.querySelector('#dialog-form');
  ++dialogGeneration;
  document.querySelector('#dialog-title').textContent = `${component.name} settings`;
  setDialogAction('Close');
  const cancel = form.querySelector('[value="cancel"]');
  if (cancel) cancel.hidden = true;
  const names = [...new Set((Array.isArray(containers) ? containers : [])
    .map(name => String(name || '').trim())
    .filter(Boolean))].sort((left, right) => left.localeCompare(right));
  const inventory = names.length
    ? `<ul class="container-list">${names.map(name => `<li><code>${escapeHTML(name)}</code></li>`).join('')}</ul>`
    : '<p class="muted">No containers were detected.</p>';
  body.innerHTML = `<p class="settings-notice">This is a read-only view of the containers currently reported by Docker. Container management remains in the owning component lifecycle.</p>${inventory}`;
  form.onsubmit = () => {};
  openDialog(dialog);
}

async function loadDiscovery(prefetched = null) {
  const generation = ++discoveryGeneration;
  const server = document.querySelector('#server-state');
  const root = document.querySelector('#components');
  if (!server || !root) return;
  try {
    if (prefetched?.error) throw prefetched.error;
    const d = prefetched?.value || await api('/api/discovery');
    if (generation !== discoveryGeneration || !server.isConnected || !root.isConnected) return;
    server.innerHTML = `<tr><td>Simple Bridge Panel</td><td><a class="version-link" href="${escapeHTML(state.repository || 'https://github.com/silenceremember/sbp-panel')}" target="_blank" rel="noreferrer">v${escapeHTML(state.version || '-')}</a></td></tr><tr><td>Operating system</td><td>${escapeHTML(d.operating_system || '-')}</td></tr><tr><td>Kernel</td><td>${escapeHTML(d.kernel || '-')}</td></tr><tr><td>Docker</td><td>${d.docker_available ? 'Installed' : 'Not installed'}</td></tr><tr><td>Containers</td><td>${d.containers?.length || 0}</td></tr>`;
    root.innerHTML = '';
    for (const component of d.components || []) {
      const row = document.createElement('tr');
      const isBypass = Boolean(BYPASS_COMPONENT_SETTINGS[component.id]);
      const componentIsActive = d.lifecycle?.status === 'running' && d.lifecycle.component_id === component.id;
      const stateLabel = component.external
        ? '<span class="state-pill external">External</span>'
        : component.installed ? '<span class="state-pill up">Installed</span>' : '<span class="state-pill">Not installed</span>';
      const version = escapeHTML(component.version || '-');
      const settingsAction = buttonHTML('Settings', 'secondary', 'data-component-settings');
      const lifecycleAction = componentIsActive
        ? buttonHTML(lifecyclePendingLabel(d.lifecycle.operation), 'secondary', 'data-lifecycle-active disabled')
        : component.external
          ? component.can_remove_external ? buttonHTML('Remove', 'danger', 'data-remove-external') : '<span class="muted">-</span>'
          : component.installed
            ? buttonHTML('Remove', 'danger', 'data-uninstall')
            : buttonHTML(component.can_install ? 'Install' : 'Unavailable', 'primary', `data-install ${component.can_install ? '' : 'disabled'}`.trim());
      const action = settingsAction + lifecycleAction;
      const blocker = component.note || (component.installed && !component.can_uninstall ? 'The component was detected but is not managed by the panel.' : '');
      row.innerHTML = `<td><b>${escapeHTML(component.name)}</b></td><td>${version}</td><td>${stateLabel}</td><td><div class="install-note"><span>${escapeHTML(component.description || 'Managed panel component.')}</span>${blocker ? `<strong>${escapeHTML(blocker)}</strong>` : ''}</div></td><td><div class="component-actions">${action}</div></td>`;
      const button = row.querySelector('[data-install]');
      if (button) button.onclick = async () => {
        if (!component.can_install) { notify(component.note || 'Complete the setup section below first.', 'warning'); return; }
        await runComponentLifecycle(component, button, 'install');
      };
      row.querySelector('[data-remove-external]')?.addEventListener('click', () => externalRemovalDialog(component));
      row.querySelector('[data-component-settings]')?.addEventListener('click', () => {
        if (isBypass) bypassSettingsDialog(component);
        else if (component.id === 'xray' || component.id === 'xray-xhttp') xrayRealitySNIDialog(component);
        else if (component.id === 'tweaks' || component.id === 'amneziawg') componentTextSettingsDialog(component);
        else if (component.id === 'docker') dockerSettingsDialog(component, d.containers);
        else readOnlyComponentSettingsDialog(component);
      });
      row.querySelector('[data-uninstall]')?.addEventListener('click', async event => {
        const remove = event.currentTarget;
        if (!component.can_uninstall) {
          notify(blocker || `“${component.name}” cannot be removed right now.`, 'warning', 'Removal unavailable');
          return;
        }
        const warning = component.id === 'docker'
          ? `Remove “${component.name}”? The Docker package, images, volumes and local data will be permanently removed.`
          : `Remove “${component.name}”? Managed configuration and credentials for this component will be removed.`;
        if (!confirm(warning)) return;
        await runComponentLifecycle(component, remove, 'uninstall');
      });
      root.append(row);
    }
    resumeLifecycle(d.lifecycle);
  } catch (e) {
    if (generation !== discoveryGeneration || !server.isConnected || !root.isConnected) return;
    server.innerHTML = '<tr><td>Agent</td><td>Unavailable</td></tr>';
    notify(`Agent unavailable: ${e.message}`, 'error');
  }
}

function applyGroupMetrics(metrics) {
  const devices = state?.devices || [];
  const liveDevices = new Map((metrics?.managed_devices || []).map(device => [
    Number(device.device_id), Number(device.rx_bytes || 0) + Number(device.tx_bytes || 0)
  ]));
  for (const device of devices) {
    const target = document.querySelector(`[data-device-traffic="${Number(device.id)}"]`);
    if (target && liveDevices.has(Number(device.id))) target.textContent = fmtBytes(liveDevices.get(Number(device.id)));
  }
  const deviceTraffic = new Map();
  const groupsWithBypass = new Set();
  for (const device of devices) {
    const groupID = Number(device.group_id);
    if (String(device.method || '').startsWith('bypass-')) {
      groupsWithBypass.add(groupID);
      continue;
    }
    if (device.method !== 'xray' && device.method !== 'xray-xhttp' && device.method !== 'amneziawg') continue;
    const total = liveDevices.has(Number(device.id))
      ? liveDevices.get(Number(device.id))
      : Number(device.rx_bytes || 0) + Number(device.tx_bytes || 0);
    deviceTraffic.set(groupID, (deviceTraffic.get(groupID) || 0) + total);
  }
  const baseTraffic = new Map((state?.groups || []).map(group => {
    const groupID = Number(group.id);
    const persisted = Number(group.rx_bytes || 0) + Number(group.tx_bytes || 0);
    return [groupID, groupsWithBypass.has(groupID) ? persisted : (deviceTraffic.get(groupID) || 0)];
  }));
  document.querySelectorAll('[data-group-traffic]').forEach(target => {
    target.textContent = fmtBytes(baseTraffic.get(Number(target.dataset.groupTraffic)) || 0);
  });
  document.querySelectorAll('[data-group-traffic-details]').forEach(target => {
    target.textContent = '';
  });
  const roomTraffic = new Map();
  const roomDetails = new Map();
  for (const room of (metrics?.bypass_rooms || [])) {
    const groupID = Number(room.group_id);
    const total = Number(room.rx_bytes || 0) + Number(room.tx_bytes || 0);
    roomTraffic.set(groupID, (roomTraffic.get(groupID) || 0) + total);
    const details = roomDetails.get(groupID) || [];
    details.push(`${String(room.provider || '').replace('bypass-', '').toUpperCase()}: ${fmtBytes(total)}`);
    roomDetails.set(groupID, details);
  }
  for (const [groupID, total] of roomTraffic) {
    const target = document.querySelector(`[data-group-traffic="${groupID}"]`);
    if (target) target.textContent = fmtBytes((deviceTraffic.get(groupID) || 0) + total);
    const details = document.querySelector(`[data-group-traffic-details="${groupID}"]`);
    if (details) details.textContent = roomDetails.get(groupID).join(' · ');
  }
}

function startMetricsPolling() {
  stopMetricsPolling();
  const generation = metricsPollGeneration;
  const schedule = delay => {
    if (generation === metricsPollGeneration && state) metricsTimer = setTimeout(poll, delay);
  };
  const poll = async () => {
    if (generation !== metricsPollGeneration || !state) return;
    if (document.hidden || metricsLoading) {
      schedule(10000);
      return;
    }
    metricsLoading = true;
    try { await loadMetrics(true); }
    finally {
      metricsLoading = false;
      schedule(10000);
    }
  };
  schedule(10000);
}

async function loadMetrics(quiet = false, prefetched = null) {
  const generation = ++metricsRequestGeneration;
  const root = document.querySelector('#server-metrics');
  if (!root) return;
  try {
    if (prefetched?.error) throw prefetched.error;
    const m = prefetched?.value || await api('/api/metrics');
    if (generation !== metricsRequestGeneration || !root.isConnected) return;
    lastMetrics = m;
    const monthTotal = Number(m.month_rx_bytes || 0) + Number(m.month_tx_bytes || 0);
    applyGroupMetrics(m);
    root.innerHTML = `
      <tr><td>Uptime</td><td>${escapeHTML(fmtUptime(m.uptime_seconds))}</td></tr>
      <tr><td>CPU</td><td>${Number(m.cpu_percent || 0).toFixed(1)}% · load ${Number(m.load_1 || 0).toFixed(2)}</td></tr>
      <tr><td>Memory</td><td>${fmtBytes(m.memory_used_bytes)} / ${fmtBytes(m.memory_total_bytes)} · ${fmtPercent(m.memory_used_bytes, m.memory_total_bytes)}</td></tr>
      <tr><td>Disk</td><td>${fmtBytes(m.disk_used_bytes)} / ${fmtBytes(m.disk_total_bytes)} · ${fmtPercent(m.disk_used_bytes, m.disk_total_bytes)}</td></tr>
      <tr><td>Current network · ${escapeHTML(m.interface || '-')}</td><td>↓ ${fmtBytes(m.rx_bytes_per_second)}/s · ↑ ${fmtBytes(m.tx_bytes_per_second)}/s</td></tr>
      <tr><td>Server traffic for ${escapeHTML(m.month_key || 'month')}</td><td>↓ ${fmtBytes(m.month_rx_bytes)} · ↑ ${fmtBytes(m.month_tx_bytes)} · total ${fmtBytes(monthTotal)}</td></tr>`;
  } catch (e) {
    if (generation !== metricsRequestGeneration || !root.isConnected) return;
    lastMetrics = null;
    applyGroupMetrics(null);
    root.innerHTML = '<tr><td>Monitoring</td><td>Unavailable</td></tr>';
    if (!quiet) notify(`Monitoring unavailable: ${e.message}`, 'error');
  }
}

async function watchJob(id, button, operation, options = {}) {
  const deadline = Date.now() + 10 * 60 * 1000;
  const statusPath = options.statusPath || `/api/components/${encodeURIComponent(id)}/install`;
  let delay = 1200;
  let transientFailures = 0;
  while (Date.now() < deadline) {
    await sleep(delay);
    let value;
    try {
      value = await api(statusPath);
      transientFailures = 0;
    } catch (error) {
      if (error?.status || ++transientFailures >= 5) throw error;
      delay = Math.min(3000, Math.ceil(delay * 1.35));
      continue;
    }
    const job = value?.job && typeof value.job === 'object' ? value.job : value;
    if (job?.status === 'running' || job?.status === 'queued' || job?.status === 'pending') {
      delay = Math.min(3000, Math.ceil(delay * 1.35));
      continue;
    }
    if (job?.status === 'done' || job?.status === 'success') {
      if (options.onDone) await options.onDone(job, value);
      else {
        if (!document.querySelector('#components')) return job;
        await load();
        notify(job.output || (operation === 'uninstall' ? 'Component removed.' : 'Component installed.'), 'success');
      }
      return job;
    }
    const message = job?.error || `${operation === 'uninstall' ? 'Removal' : operation === 'install' ? 'Installation' : 'Operation'} failed`;
    if (options.throwOnError) throw new Error(message);
    button.disabled = false;
    button.textContent = 'Retry';
    notify(message, 'error');
    return job;
  }
  const label = operation === 'uninstall' ? 'Removal' : operation === 'install' ? 'Installation' : 'Operation';
  throw new Error(`${label} is still running after ten minutes. Refresh the dashboard to check its status.`);
}

function renderBypassRooms(root, provider) {
  if (!root || !provider) return;
  const rooms = bypassRooms.filter(room => room.provider === provider);
  if (!rooms.length) {
    root.innerHTML = '<span class="muted">No saved rooms for this service.</span>';
    return;
  }
  root.innerHTML = rooms.map(room => `<span><b>${escapeHTML(room.group_name)}</b> · <span class="saved-room-code">${escapeHTML(room.code)}</span></span>`).join('');
}

async function loadBypassRooms(prefetched = null) {
  const generation = ++bypassRoomsGeneration;
  const root = document.querySelector('[data-bypass-rooms]');
  try {
    if (prefetched?.error) throw prefetched.error;
    const value = prefetched?.value || await api('/api/bypass/rooms');
    if (generation !== bypassRoomsGeneration) return false;
    bypassRooms = Array.isArray(value?.rooms) ? value.rooms : [];
    if (root?.isConnected) {
      const component = Object.values(BYPASS_COMPONENT_SETTINGS).find(item => document.querySelector('#dialog-title')?.textContent === `${item.label} settings`);
      renderBypassRooms(root, component?.provider);
    }
    return true;
  } catch (error) {
    if (generation !== bypassRoomsGeneration) return false;
    bypassRooms = [];
    if (root?.isConnected) root.innerHTML = `<span class="error">${escapeHTML(error.message || 'Could not load rooms.')}</span>`;
    return false;
  }
}

window.addEventListener('unhandledrejection', event => {
  event.preventDefault();
  notifyError(event.reason || 'Unhandled error');
});

boot().catch(e => {
  app.innerHTML = '<section class="center"><div class="card"><h1>Simple Bridge Panel</h1><p class="muted">Failed to load the panel.</p></div></section>';
  notifyError(e);
});
