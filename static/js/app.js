function currentLocale() {
    return (window.WP_PANEL_I18N && window.WP_PANEL_I18N.lang) || document.body?.dataset.lang || 'zh-CN';
}

function t(key, params = {}) {
    const messages = (window.WP_PANEL_I18N && window.WP_PANEL_I18N.messages) || {};
    let message = messages[key] || key;
    Object.entries(params).forEach(([name, value]) => {
        message = message.split('{{' + name + '}}').join(String(value));
    });
    return message;
}

// Render the small Markdown subset used by AI responses. All source text is
// escaped before controlled tags are added, so callers may safely use x-html.
function renderSafeMarkdown(value) {
    const escaped = String(value || '')
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    const inline = text => text
        .replace(/\[([^\]]+)\]\(([^)]+)\)/g, (all, label, href) => {
            const normalized = href.replace(/&amp;/g, '&').trim();
            if (normalized.includes('\\') || !/^(https?:\/\/|\/)/i.test(normalized)) return label;
            return `<a class="text-blue-400 hover:underline" href="${href}" target="_blank" rel="noopener noreferrer">${label}</a>`;
        })
        .replace(/`([^`]+)`/g, '<code class="font-mono text-blue-200 bg-gray-950 px-1 py-0.5">$1</code>')
        .replace(/\*\*([^*]+)\*\*/g, '<strong class="text-white font-semibold">$1</strong>')
        .replace(/(^|[^*])\*([^*]+)\*/g, '$1<em>$2</em>');
    const lines = escaped.split(/\r?\n/), html = [];
    let list = '', inCode = false, codeLines = [];
    const closeList = () => { if (list) { html.push(`</${list}>`); list = ''; } };
    const closeCode = () => {
        if (!inCode) return;
        html.push(`<pre class="font-mono text-xs text-gray-300 bg-gray-950 p-3 rounded overflow-auto whitespace-pre"><code>${codeLines.join('\n')}</code></pre>`);
        inCode = false; codeLines = [];
    };
    const tableCells = line => line.replace(/^\||\|$/g, '').split('|').map(cell => cell.trim());
    for (let index = 0; index < lines.length; index++) {
        const raw = lines[index];
        const line = raw.trim();
        let match;
        if (/^```/.test(line)) {
            closeList();
            if (inCode) closeCode(); else inCode = true;
            continue;
        }
        if (inCode) { codeLines.push(raw); continue; }
        if (!line) { closeList(); continue; }
        if ((match = line.match(/^(#{1,3})\s+(.+)$/))) {
            closeList();
            const size = match[1].length === 1 ? 'text-lg' : (match[1].length === 2 ? 'text-base' : 'text-sm');
            html.push(`<h${match[1].length} class="${size} text-white font-semibold mt-4 mb-2">${inline(match[2])}</h${match[1].length}>`);
        } else if (line.includes('|') && index + 1 < lines.length && /^\s*\|?\s*:?-{3,}/.test(lines[index + 1])) {
            closeList();
            const headers = tableCells(line);
            index += 2;
            const rows = [];
            while (index < lines.length && lines[index].includes('|') && lines[index].trim()) {
                rows.push(tableCells(lines[index])); index++;
            }
            index--;
            html.push('<div class="overflow-x-auto"><table class="w-full text-xs border-collapse"><thead><tr>' + headers.map(cell => `<th class="text-left text-gray-200 border border-gray-700 p-2">${inline(cell)}</th>`).join('') + '</tr></thead><tbody>' + rows.map(row => '<tr>' + row.map(cell => `<td class="text-gray-400 border border-gray-800 p-2">${inline(cell)}</td>`).join('') + '</tr>').join('') + '</tbody></table></div>');
        } else if ((match = line.match(/^[-*]\s+(.+)$/))) {
            if (list !== 'ul') { closeList(); list = 'ul'; html.push('<ul class="list-disc pl-5 space-y-1">'); }
            html.push(`<li>${inline(match[1])}</li>`);
        } else if ((match = line.match(/^\d+[.)]\s+(.+)$/))) {
            if (list !== 'ol') { closeList(); list = 'ol'; html.push('<ol class="list-decimal pl-5 space-y-1">'); }
            html.push(`<li>${inline(match[1])}</li>`);
        } else if ((match = line.match(/^&gt;\s*(.+)$/))) {
            closeList(); html.push(`<blockquote class="border-l-2 border-blue-800 pl-3 text-gray-400">${inline(match[1])}</blockquote>`);
        } else if (/^([-*_])\1\1+$/.test(line)) {
            closeList(); html.push('<hr class="border-gray-800 my-3">');
        } else { closeList(); html.push(`<p>${inline(line)}</p>`); }
    }
    closeCode();
    closeList();
    return html.join('');
}

function api(path, options = {}) {
    const prefix = document.body.dataset.panelPrefix || '';
    const url = prefix + '/api' + path;
    const { silent = false, suppressToast = false, timeout = 0, ...fetchOptions } = options;
    let timeoutID = null;
    let externalAbortHandler = null;
    let timedOut = false;
    if (timeout > 0) {
        const externalSignal = fetchOptions.signal;
        const controller = new AbortController();
        fetchOptions.signal = controller.signal;
        if (externalSignal) {
            if (externalSignal.aborted) {
                controller.abort();
            } else {
                externalAbortHandler = () => controller.abort();
                externalSignal.addEventListener('abort', externalAbortHandler, { once: true });
            }
        }
        timeoutID = setTimeout(() => {
            timedOut = true;
            controller.abort();
        }, timeout);
    }

    const headers = {
        'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]')?.content || '',
        ...fetchOptions.headers,
    };

    if (fetchOptions.body && typeof fetchOptions.body === 'object' && !(fetchOptions.body instanceof FormData)) {
        headers['Content-Type'] = 'application/json';
        fetchOptions.body = JSON.stringify(fetchOptions.body);
    }

    return fetch(url, { ...fetchOptions, headers })
        .then(async (resp) => {
            if (resp.status === 401 && path !== '/auth/login') {
                window.location.href = prefix + '/login';
                const err = new Error(t('auth.session_expired'));
                err.status = resp.status;
                throw err;
            }
            if (resp.status === 503) {
                const err = new Error(t('common.service_busy'));
                err.status = resp.status;
                throw err;
            }
            const contentType = resp.headers.get('content-type') || '';
            if (!contentType.includes('application/json')) {
                const text = await resp.text();
                console.error('Non-JSON response:', resp.status, text.substring(0, 200));
                const err = new Error(t('common.service_exception', { status: resp.status }));
                err.status = resp.status;
                throw err;
            }
            const data = await resp.json();
            if (!resp.ok) {
                console.error('API error:', resp.status, data);
                const err = new Error(data.message || 'Request failed (' + resp.status + ')');
                err.status = resp.status;
                if (data.conflicts) err.conflicts = data.conflicts;
                throw err;
            }
            if (!data.success) {
                const err = new Error(data.message || t('common.operation_failed'));
                if (data.conflicts) err.conflicts = data.conflicts;
                throw err;
            }
            return data;
        })
        .catch(err => {
            if (timedOut) {
                err = new Error(t('common.request_timeout'));
            }
            const message = friendlyAPIError(err);
            const displayErr = message === err.message ? err : new Error(message);
            if (err.conflicts) displayErr.conflicts = err.conflicts;
            if (Number.isInteger(err.status)) displayErr.status = err.status;
            if (message !== t('auth.session_expired') && !displayErr.conflicts && !silent && !suppressToast) {
                console.error('Fetch failed:', err.message, 'URL:', url);
                showToast(displayErr.message, 'error');
            }
            throw displayErr;
        })
        .finally(() => {
            if (timeoutID) clearTimeout(timeoutID);
            if (externalAbortHandler && options.signal) options.signal.removeEventListener('abort', externalAbortHandler);
        });
}

const __apiGetCache = new Map();

function cachedAPI(path, options = {}, ttlMs = 1000) {
    const method = (options.method || 'GET').toUpperCase();
    if (method !== 'GET' || options.body) {
        return api(path, options);
    }
    const now = Date.now();
    const cached = __apiGetCache.get(path);
    if (cached && cached.expiresAt > now) {
        return cached.promise;
    }
    const promise = api(path, options).finally(() => {
        setTimeout(() => {
            const current = __apiGetCache.get(path);
            if (current && current.promise === promise) {
                __apiGetCache.delete(path);
            }
        }, ttlMs);
    });
    __apiGetCache.set(path, { promise, expiresAt: now + ttlMs });
    return promise;
}

function deferTask(fn, delay = 0) {
    const run = () => {
        try {
            fn();
        } catch (e) {
            console.error(e);
        }
    };
    if (delay > 0) {
        setTimeout(run, delay);
        return;
    }
    if ('requestIdleCallback' in window) {
        requestIdleCallback(run, { timeout: 1500 });
        return;
    }
    setTimeout(run, 0);
}

function friendlyAPIError(err) {
    const message = err && err.message ? err.message : '';
    if (/Load failed|Failed to fetch|NetworkError|Network request failed|fetch failed/i.test(message)) {
        return t('common.network_error');
    }
    if (/AbortError|The operation was aborted/i.test(message)) {
        return t('common.request_cancelled');
    }
    return message || t('common.request_failed');
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function fmtTime(t) {
    if (!t) return '--';
    // Handles both RFC 3339 (2026-05-22T05:48:55Z) and SQLite (2026-05-22 05:48:55)
    return new Date(t.replace(' ', 'T')).toLocaleString(currentLocale());
}

function formatUptime(seconds) {
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const parts = [];
    if (d > 0) parts.push(d + 'd');
    if (h > 0) parts.push(h + 'h');
    if (m > 0) parts.push(m + 'm');
    return parts.join(' ') || '<1m';
}

function showToast(message, type = 'info') {
    const colors = {
        success: 'background:#065f46;border-color:#059669;color:#a7f3d0;',
        error: 'background:#991b1b;border-color:#dc2626;color:#fecaca;',
        warning: 'background:#78350f;border-color:#d97706;color:#fde68a;',
        info: 'background:#1e3a5f;border-color:#2563eb;color:#bfdbfe;',
    };
    const toast = document.createElement('div');
    toast.style.cssText = 'position:fixed;bottom:80px;left:50%;transform:translateX(-50%);z-index:9998;padding:12px 24px;border:1px solid;transition:opacity 0.3s;max-width:min(760px,calc(100vw - 32px));max-height:45vh;overflow:auto;white-space:pre-wrap;word-break:break-word;' + (colors[type] || colors.info);
    toast.textContent = message;
    document.body.appendChild(toast);
    setTimeout(() => {
        toast.style.opacity = '0';
        setTimeout(() => toast.remove(), 300);
    }, 5000);
}

function confirmModal(message) {
    return new Promise((resolve) => {
        const overlay = document.createElement('div');
        overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.75);display:flex;align-items:center;justify-content:center;z-index:9999;';
        overlay.innerHTML = `
            <div style="background:#1f2937;border:1px solid #374151;padding:24px;max-width:32rem;width:100%;margin:0 16px;max-height:80vh;display:flex;flex-direction:column;">
                <p id="modal-message" style="color:#e5e7eb;margin-bottom:16px;white-space:pre-wrap;overflow-y:auto;flex:1;min-height:0;"></p>
                <div style="display:flex;justify-content:flex-end;gap:12px;flex-shrink:0;">
                    <button id="modal-cancel" style="background:#4b5563;color:#fff;border:none;padding:8px 16px;cursor:pointer;font-size:14px;">${t('common.cancel')}</button>
                    <button id="modal-confirm" style="background:#dc2626;color:#fff;border:none;padding:8px 16px;cursor:pointer;font-size:14px;">${t('common.confirm')}</button>
                </div>
            </div>
        `;
        overlay.querySelector('#modal-message').textContent = message;
        document.body.appendChild(overlay);
        overlay.querySelector('#modal-cancel').onclick = () => { overlay.remove(); resolve(false); };
        overlay.querySelector('#modal-confirm').onclick = () => { overlay.remove(); resolve(true); };
        overlay.onclick = (e) => { if (e.target === overlay) { overlay.remove(); resolve(false); } };
    });
}

function alertModal(message) {
    return new Promise((resolve) => {
        const overlay = document.createElement('div');
        overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.75);display:flex;align-items:center;justify-content:center;z-index:9999;';
        overlay.innerHTML = `
            <div style="background:#1f2937;border:1px solid #374151;padding:24px;max-width:32rem;width:100%;margin:0 16px;max-height:80vh;display:flex;flex-direction:column;">
                <p id="modal-message" style="color:#e5e7eb;margin-bottom:16px;white-space:pre-wrap;overflow-y:auto;flex:1;min-height:0;"></p>
                <div style="display:flex;justify-content:flex-end;gap:12px;flex-shrink:0;">
                    <button id="modal-close" style="background:#4b5563;color:#fff;border:none;padding:8px 16px;cursor:pointer;font-size:14px;">${t('dashboard.close')}</button>
                </div>
            </div>
        `;
        overlay.querySelector('#modal-message').textContent = message;
        document.body.appendChild(overlay);
        overlay.querySelector('#modal-close').onclick = () => { overlay.remove(); resolve(); };
        overlay.onclick = (e) => { if (e.target === overlay) { overlay.remove(); resolve(); } };
    });
}
