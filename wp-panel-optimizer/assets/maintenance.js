/* Passwords live only in the form until submission, never in persistent state. */
(() => {
    'use strict';
    const cfg = window.WPPMaintenance;
    const dialog = document.getElementById('wpp-maintenance-dialog');
    if (!cfg || !dialog) return;
    const text = cfg.text;
    const form = document.getElementById('wpp-maintenance-form');
    const password = document.getElementById('wpp-maintenance-password');
    const actions = document.getElementById('wpp-maintenance-actions');
    const message = document.getElementById('wpp-maintenance-message');
    const stateLabel = document.getElementById('wpp-maintenance-state');
    const bar = document.querySelector('#wp-admin-bar-wpp-maintenance > a');
    const warning = document.getElementById('wpp-maintenance-warning');
    let state = { state: 'unknown' }, offset = 0, busy = false, nextPoll = 0;
    let pending = null, actionKey = '', polling = false, issued = 0, accepted = 0;
    const now = () => Math.floor(Date.now() / 1000) + offset;
    const requestID = () => {
        const bytes = crypto.getRandomValues(new Uint8Array(16));
        bytes[6] = (bytes[6] & 15) | 64; bytes[8] = (bytes[8] & 63) | 128;
        const hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
        return [hex.slice(0, 8), hex.slice(8, 12), hex.slice(12, 16), hex.slice(16, 20), hex.slice(20)].join('-');
    };
    function button(label, operation, minutes = 0) {
        const el = document.createElement('button');
        el.type = 'button'; el.textContent = label; el.disabled = busy;
        el.className = 'wpp-maintenance-button' + (operation === 'unlock' || operation === 'relock' ? ' is-primary' : '');
        el.addEventListener('click', () => run(operation, minutes));
        actions.appendChild(el);
    }
    function render() {
        const remaining = Math.max(0, (state.expires_at || 0) - now());
        let label = text[state.state] || text.unknown;
        if (state.state === 'unlocked') label += ' · ' + Math.floor(remaining / 60) + ':' + String(remaining % 60).padStart(2, '0');
        if (bar) {
            bar.textContent = 'WP Panel: ' + label;
            if (bar.parentElement.dataset) bar.parentElement.dataset.state = remaining <= 60 && state.state === 'unlocked' ? 'warning' : state.state;
        }
        stateLabel.textContent = busy ? text.busy : label;
        if (dialog.dataset) dialog.dataset.state = remaining <= 60 && state.state === 'unlocked' ? 'warning' : state.state;
        warning.textContent = (state.notice === 'restart' ? text.restart + ' ' : '') + text.warning;
        warning.classList.toggle('wpp-warning', state.state === 'unlocked' && remaining <= 60);
        const key = [busy, state.state, state.enabled, remaining > 0, state.window_id].join('|');
        if (key !== actionKey) {
            actionKey = key; actions.replaceChildren();
            if (state.state === 'locked' && state.enabled) button(text.unlock, 'unlock');
            if (state.state === 'unlocked' && remaining > 0) {
                for (const minutes of [1, 3, 5]) button(text.extend + ' ' + minutes + ' ' + text.minute, 'extend', minutes);
            }
            if (state.window_id) button(text.relock, 'relock');
        }
        password.disabled = busy;
        password.hidden = busy;
        if (!busy && !state.enabled && !state.window_id) message.textContent = text.disabled;
    }
    async function request(operation, data) {
        const sequence = ++issued;
        const body = new URLSearchParams({ action: 'wpp_maintenance', nonce: cfg.nonce, operation, ...data });
        const response = await fetch(cfg.url, { method: 'POST', credentials: 'same-origin', body, cache: 'no-store' });
        const result = await response.json();
        if (!result.success) {
            const error = new Error(result.data?.message || 'state_unknown');
            error.definitive = ['verification_failed', 'verification_frozen', 'password_required', 'invalid_request', 'operation_unavailable', 'lock_mode_required'].includes(error.message);
            throw error;
        }
        if (sequence < accepted) return;
        accepted = sequence;
        state = result.data; offset = state.server_time - Math.floor(Date.now() / 1000);
        if (pending && (state.window_id !== pending.window_id || state.revision > pending.revision)) pending = null;
        nextPoll = Date.now() + (state.state === 'locked' || state.state === 'unlocked_permanent' ? 30000 : 5000);
    }
    async function run(operation, minutes) {
        if (busy) return;
        const needsPassword = operation === 'unlock' || (operation === 'extend' && state.expires_at + minutes * 60 > state.verified_until);
        if (needsPassword && !password.value) { message.textContent = text.password_required; password.focus(); return; }
        if (!pending || pending.operation !== operation || pending.minutes !== minutes || pending.window_id !== (state.window_id || '')) {
            pending = { operation, minutes, window_id: state.window_id || '', request_id: requestID(), revision: state.revision || 0 };
        }
        const data = { ...pending, password: password.value }; delete data.operation;
        password.value = ''; busy = true; message.textContent = ''; render();
        try {
            await request(operation, data); pending = null;
        } catch (error) {
            if (error.definitive) pending = null;
            message.textContent = text[error.message] || text.state_unknown;
            try { await request('status', {}); } catch (_) { state = { state: 'unknown' }; }
        } finally { delete data.password; busy = false; render(); }
    }
    form.addEventListener('submit', event => event.preventDefault());
    document.getElementById('wpp-maintenance-password-label').textContent = text.password;
    document.getElementById('wpp-maintenance-close').textContent = text.close;
    document.getElementById('wpp-maintenance-close').onclick = () => { password.value = ''; dialog.close(); };
    dialog.addEventListener('close', () => { password.value = ''; });
    function open(event) { event.preventDefault(); if (!dialog.open) dialog.showModal(); render(); }
    if (bar) bar.addEventListener('click', open);
    document.querySelectorAll('[data-wpp-maintenance-open]').forEach(el => el.addEventListener('click', open));
    async function poll() {
        if (!busy && !polling && Date.now() >= nextPoll) {
            polling = true;
            nextPoll = Date.now() + 5000;
            try { await request('status', {}); } catch (_) { state = { state: 'unknown' }; }
            finally { polling = false; }
        }
        render();
    }
    poll(); setInterval(poll, 1000);
})();
