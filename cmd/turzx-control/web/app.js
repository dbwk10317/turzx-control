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
const summaryState = document.querySelector('#summary-state');
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

// Live regions re-announce on every DOM mutation, so only write when changed.
function setText(node, value) {
  if (node.textContent !== value) node.textContent = value;
}

function hide(node, hidden) {
  if (hidden && document.activeElement === node) node.closest('.card-actions')?.querySelector('button:not(.hidden)')?.focus();
  node.classList.toggle('hidden', hidden);
}

function renderProvider(value, status, message, button, disconnect, stateMessage, name) {
  value ||= 'disconnected';
  status.dataset.status = value;
  setText(status.lastChild, ` ${labels[value] || '확인 필요'}`);
  if (stateMessage) setText(message, stateMessage);
  const busy = value === 'starting' || value === 'waiting' || value === 'disconnecting';
  button.disabled = busy || value === 'connected' || value === 'installed';
  button.setAttribute('aria-busy', busy ? 'true' : 'false');
  setText(button, value === 'connected' || value === 'installed' ? '연결 완료' : value === 'waiting' ? '로그인 대기 중' : `${name} 연결`);
  hide(disconnect, value !== 'connected' && value !== 'installed' && value !== 'disconnecting' && value !== 'error');
  disconnect.disabled = value === 'disconnecting';
  disconnect.setAttribute('aria-busy', value === 'disconnecting' ? 'true' : 'false');
  setText(disconnect, value === 'disconnecting' ? '해제 중' : '연결 해제');
}

function render(state) {
  const display = state.display || {};
  const outputStatus = display.status || 'disabled';
  displayStatus.dataset.status = outputStatus;
  setText(displayStatus.lastChild, ` ${displayLabels[outputStatus] || '확인 필요'}`);
  setText(summaryState, displayLabels[outputStatus] || '확인 필요');
  setText(displayMessage, display.message || '');
  const hardware = state.dashboard?.Hardware;
  for (const name of ['CPU', 'GPU', 'RAM']) {
    const metric = hardware?.[name];
    setText(document.querySelector(`#${name.toLowerCase()}-live`), metric ? `${metric.Usage} · ${metric.Temperature}` : '—');
  }
  setText(document.querySelector('#ram-label'), hardware?.RAM?.Label || 'RAM');
  const value = state.codex_status || 'disconnected';
  renderProvider(value, codexStatus, codexMessage, codexConnect, codexDisconnect, state.codex_message, 'Codex');
  renderProvider(state.claude_status, claudeStatus, claudeMessage, claudeConnect, claudeDisconnect, state.claude_message, 'Claude');
  if (value === 'connected' || value === 'error') hide(continueLink, true);
}

async function refresh() {
  try {
    const response = await fetch('/api/state', { cache: 'no-store' });
    if (response.ok) render(await response.json());
  } catch (_) {
    setText(summaryState, '연결 끊김');
    setText(displayMessage, 'TURZX Control이 종료되었거나 응답하지 않습니다. 앱을 다시 실행한 뒤 이 페이지를 새로 고치세요.');
  }
}

codexConnect.addEventListener('click', async () => {
  codexConnect.disabled = true;
  codexConnect.setAttribute('aria-busy', 'true');
  setText(feedback, '공식 로그인 페이지를 준비하고 있습니다.');
  try {
    const response = await fetch('/api/codex/login', {
      method: 'POST',
      headers: { 'X-TURZX-Token': token },
    });
    if (!response.ok) throw new Error('login unavailable');
    const result = await response.json();
    // The URL comes from the codex CLI; only follow an https link.
    const authURL = new URL(result.auth_url);
    if (authURL.protocol !== 'https:') throw new Error('unexpected login URL');
    continueLink.href = authURL.href;
    hide(continueLink, false);
    setText(feedback, '브라우저에서 계속을 눌러 로그인하세요. 완료 상태는 자동으로 반영됩니다.');
    continueLink.focus();
  } catch (_) {
    setText(feedback, '로그인을 시작하지 못했습니다. Codex 설치 상태를 확인한 뒤 다시 시도하세요.');
  }
  await refresh();
});

async function disconnect(provider, button) {
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  setText(feedback, `${provider === 'codex' ? 'Codex' : 'Claude'} 전용 프로필 연결을 해제하고 있습니다.`);
  try {
    const response = await fetch(`/api/${provider}/logout`, {
      method: 'POST',
      headers: { 'X-TURZX-Token': token },
    });
    if (!response.ok) throw new Error('logout unavailable');
    setText(feedback, '연결 해제를 시작했습니다. 완료 상태는 자동으로 반영됩니다.');
  } catch (_) {
    setText(feedback, '연결을 해제하지 못했습니다. 잠시 후 다시 시도하세요.');
  }
  await refresh();
}

codexDisconnect.addEventListener('click', () => disconnect('codex', codexDisconnect));
claudeDisconnect.addEventListener('click', () => disconnect('claude', claudeDisconnect));

claudeConnect.addEventListener('click', async () => {
  claudeConnect.disabled = true;
  claudeConnect.setAttribute('aria-busy', 'true');
  setText(feedback, '전용 statusline을 준비하고 Claude 로그인을 시작합니다.');
  try {
    const response = await fetch('/api/claude/login', {
      method: 'POST',
      headers: { 'X-TURZX-Token': token },
    });
    if (!response.ok) throw new Error('login unavailable');
    setText(feedback, '열린 브라우저에서 Claude 로그인을 완료하세요. 완료 상태는 자동으로 반영됩니다.');
  } catch (_) {
    setText(feedback, '로그인을 시작하지 못했습니다. Claude와 statusline 어댑터 설치 상태를 확인하세요.');
  }
  await refresh();
});

refresh();
setInterval(refresh, 1500);
