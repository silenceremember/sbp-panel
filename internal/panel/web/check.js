const form = document.querySelector('#check-form');
const nameInput = document.querySelector('#group-name');
const button = document.querySelector('#check-button');
const result = document.querySelector('#check-result');
let currentGroup = null;
let countdownTimer = 0;

function remaining(group) {
  if (group.unlimited) return 'Unlimited access';
  if (!group.expires_at) return 'No expiration date';
  const milliseconds = new Date(group.expires_at) - Date.now();
  if (milliseconds <= 0) return 'Expired';
  const days = Math.floor(milliseconds / 86400000);
  const hours = Math.floor((milliseconds % 86400000) / 3600000);
  const minutes = Math.floor((milliseconds % 3600000) / 60000);
  return `${days} d ${hours} h ${minutes} min remaining`;
}

function renderGroup(group) {
  currentGroup = group;
  const active = group.unlimited || group.status !== 'expired';
  const expiration = group.unlimited
    ? 'Unlimited'
    : group.expires_at
      ? new Date(group.expires_at).toLocaleString('en-GB', {dateStyle: 'medium', timeStyle: 'short'})
      : 'Not set';
  result.className = `check-result ${active ? 'check-active' : 'check-expired'}`;
  result.replaceChildren();

  const heading = document.createElement('div');
  heading.className = 'check-result-heading';
  const name = document.createElement('strong');
  name.textContent = group.name;
  const status = document.createElement('span');
  status.className = `check-status ${active ? 'up' : 'down'}`;
  status.textContent = active ? 'Active' : 'Expired';
  heading.append(name, status);

  const details = document.createElement('dl');
  const expiresLabel = document.createElement('dt');
  expiresLabel.textContent = 'Expires';
  const expiresValue = document.createElement('dd');
  expiresValue.textContent = expiration;
  details.append(expiresLabel, expiresValue);
  if (!group.unlimited) {
    const remainingLabel = document.createElement('dt');
    remainingLabel.textContent = 'Time remaining';
    const remainingValue = document.createElement('dd');
    remainingValue.id = 'check-remaining';
    remainingValue.textContent = remaining(group);
    details.append(remainingLabel, remainingValue);
  }
  result.append(heading, details);
  result.hidden = false;
}

function renderError(message) {
  currentGroup = null;
  result.className = 'check-result check-expired';
  result.textContent = message;
  result.hidden = false;
}

async function checkName(name) {
  if (!name) return;
  button.disabled = true;
  button.textContent = 'Checking…';
  try {
    const response = await fetch('/api/check-group', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({name}),
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || 'Could not check the group.');
    renderGroup(payload.group);
  } catch (error) {
    renderError(error.message || 'Could not check the group.');
  } finally {
    button.disabled = false;
    button.textContent = 'Check status';
  }
}

form.addEventListener('submit', async event => {
  event.preventDefault();
  await checkName(nameInput.value.trim());
});

const directPrefix = '/check/';
if (window.location.pathname.startsWith(directPrefix)) {
  try {
    const directName = decodeURIComponent(window.location.pathname.slice(directPrefix.length)).replaceAll('_', ' ').replace(/\s+/g, ' ').trim();
    if (directName) {
      nameInput.value = directName;
      nameInput.removeAttribute('autofocus');
      checkName(directName);
    }
  } catch {
    renderError('The group link is invalid.');
  }
}

countdownTimer = window.setInterval(() => {
  if (!currentGroup) return;
  const field = document.querySelector('#check-remaining');
  if (field) field.textContent = remaining(currentGroup);
}, 30000);

window.addEventListener('pagehide', () => window.clearInterval(countdownTimer));
