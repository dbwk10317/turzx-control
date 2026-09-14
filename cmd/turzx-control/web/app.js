// SPDX-License-Identifier: GPL-3.0-or-later
const token = document.querySelector('meta[name="turzx-token"]').content;
const codexConnect = document.querySelector('#codex-connect');
const continueLink = document.querySelector('#codex-continue');
const codexDisconnect = document.querySelector('#codex-disconnect');
const codexStatus = document.querySelector('#codex-status');
const codexMessage = document.querySelector('#codex-message');
const claudeConnect = document.querySelector('#claude-connect');
const claudeStatus = document.querySelector('#claude-status');
const claudeMessage = document.querySelector('#claude-message');
const claudeDisconnect = document.querySelector('#claude-disconnect');
const feedback = document.querySelector('#login-feedback');
const displayStatus = document.querySelector('#display-status');
const displayMessage = document.querySelector('#display-message');
const displayLabels = { disabled: '출력 꺼짐', starting: '연결 중', video: '영상 전송 중', fallback: '정적 화면', disconnected: '재연결 대기', error: '확인 필요', stopped: '출력 중단' };

const labels = {
  disconnected: '연결 필요',
  starting: '준비 중',
  waiting: '로그인 대기',
  connected: '연결됨',
  installed: '수신 대기',
  disconnecting: '해제 중',
  error: '확인 필요',
};

function renderProvider(value, status, message, button, disconnect, stateMessage, name) {
  value ||= 'disconnected';
  status.dataset.status = value;
  status.lastChild.textContent = ` ${labels[value] || '확인 필요'}`;
  if (stateMessage) message.textContent = stateMessage;
  const busy = value === 'starting' || value === 'waiting' || value === 'disconnecting';
  button.disabled = busy || value === 'connected' || value === 'installed';
  button.setAttribute('aria-busy', busy ? 'true' : 'false');
  button.textContent = value === 'connected' || value === 'installed' ? '연결 완료' : value === 'waiting' ? '로그인 대기 중' : `${name} 연결`;
  disconnect.classList.toggle('hidden', value !== 'connected' && value !== 'installed' && value !== 'disconnecting' && value !== 'error');
  disconnect.disabled = value === 'disconnecting';
  disconnect.setAttribute('aria-busy', value === 'disconnecting' ? 'true' : 'false');
  disconnect.textContent = value === 'disconnecting' ? '해제 중' : '연결 해제';
}

function render(state) {
  const display = state.display || {};
  const outputStatus = display.status || 'disabled';
  displayStatus.dataset.status = outputStatus;
  displayStatus.lastChild.textContent = ` ${displayLabels[outputStatus] || '확인 필요'}`;
  document.querySelector('#summary-state').textContent = displayLabels[outputStatus] || '확인 필요';
  displayMessage.textContent = display.message || '';
  const hardware = state.dashboard?.Hardware;
  for (const name of ['CPU', 'GPU', 'RAM']) {
    const metric = hardware?.[name];
    document.querySelector(`#${name.toLowerCase()}-live`).textContent = metric ? `${metric.Usage} · ${metric.Temperature}` : '—';
  }
  document.querySelector('#ram-label').textContent = hardware?.RAM?.Label || 'RAM';
  const value = state.codex_status || 'disconnected';
  renderProvider(value, codexStatus, codexMessage, codexConnect, codexDisconnect, state.codex_message, 'Codex');
  renderProvider(state.claude_status, claudeStatus, claudeMessage, claudeConnect, claudeDisconnect, state.claude_message, 'Claude');
  if (value === 'connected' || value === 'error') {
    continueLink.classList.add('hidden');
    feedback.textContent = state.codex_message;
  }
  if (state.claude_status === 'installed' || state.claude_status === 'error') {
    feedback.textContent = state.claude_message;
  }
}

async function refresh() {
  const response = await fetch('/api/state', { cache: 'no-store' });
  if (response.ok) render(await response.json());
}

codexConnect.addEventListener('click', async () => {
  codexConnect.disabled = true;
  feedback.textContent = '공식 로그인 페이지를 준비하고 있습니다.';
  try {
    const response = await fetch('/api/codex/login', {
      method: 'POST',
      headers: { 'X-TURZX-Token': token },
    });
    if (!response.ok) throw new Error('login unavailable');
    const result = await response.json();
    continueLink.href = result.auth_url;
    continueLink.classList.remove('hidden');
    feedback.textContent = '브라우저에서 계속을 눌러 로그인하세요. 완료 상태는 자동으로 반영됩니다.';
    continueLink.focus();
  } catch (_) {
    feedback.textContent = '로그인을 시작하지 못했습니다. Codex 설치 상태를 확인한 뒤 다시 시도하세요.';
  }
  await refresh();
});

async function disconnect(provider, button) {
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  feedback.textContent = `${provider === 'codex' ? 'Codex' : 'Claude'} 전용 프로필 연결을 해제하고 있습니다.`;
  try {
    const response = await fetch(`/api/${provider}/logout`, {
      method: 'POST',
      headers: { 'X-TURZX-Token': token },
    });
    if (!response.ok) throw new Error('logout unavailable');
    feedback.textContent = '연결 해제를 시작했습니다. 완료 상태는 자동으로 반영됩니다.';
  } catch (_) {
    feedback.textContent = '연결을 해제하지 못했습니다. 잠시 후 다시 시도하세요.';
  }
  await refresh();
}

codexDisconnect.addEventListener('click', () => disconnect('codex', codexDisconnect));
claudeDisconnect.addEventListener('click', () => disconnect('claude', claudeDisconnect));

claudeConnect.addEventListener('click', async () => {
  claudeConnect.disabled = true;
  claudeConnect.setAttribute('aria-busy', 'true');
  feedback.textContent = '전용 statusline을 준비하고 Claude 로그인을 시작합니다.';
  try {
    const response = await fetch('/api/claude/login', {
      method: 'POST',
      headers: { 'X-TURZX-Token': token },
    });
    if (!response.ok) throw new Error('login unavailable');
    feedback.textContent = '열린 브라우저에서 Claude 로그인을 완료하세요. 완료 상태는 자동으로 반영됩니다.';
  } catch (_) {
    feedback.textContent = '로그인을 시작하지 못했습니다. Claude와 statusline 어댑터 설치 상태를 확인하세요.';
  }
  await refresh();
});

refresh();
setInterval(refresh, 1500);
