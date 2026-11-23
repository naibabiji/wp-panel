document.addEventListener('alpine:init', () => Alpine.data('portableImport', () => ({
    task: null,
    tasks: [],
    chosen: new Map(),
    manifest: null,
    manifestHint: null,
    aliasesDraft: null,
    aliasSuggestion: '',
    suggestingAlias: false,
    bundleSize: 0,
    timer: null,
    taskView: 0,
    selectionVersion: 0,
    statusUnavailable: false,
    confirmingSubmit: false,
    working: false,
    uploadingFile: '',
    sourceMode: 'upload',
    remoteLink: '',
    remoteReady: false,
    editingSourceIP: false,
    sourceIP: '',
    error: '',
    logs: null,

    async init() {
        await this.refreshList();
        const last = location.hash.slice(1);
        if (/^[a-f0-9]{32}$/.test(last) && this.tasks.some(item => item.id === last)) {
            await this.open(last);
        }
    },

    destroy() {
        clearTimeout(this.timer);
        this.taskView++;
    },

    get domain() {
        if (this.manifestHint?.source?.home) {
            try { return new URL(this.manifestHint.source.home).hostname; } catch {}
        }
        return this.task?.domain || '';
    },

    get usesHTTPLink() {
        return /^http:\/\//i.test(this.remoteLink.trim());
    },

    get aliasesText() {
        if (this.aliasesDraft !== null) return this.aliasesDraft;
        if ((this.task?.status === 'receiving' && this.task.domain === this.domain) || this.canRetry) {
            return (this.task.aliases || []).join('\n');
        }
        return this.aliasSuggestion;
    },

    get missing() {
        if (this.bundleSize) return [];
        const expected = this.manifestHint
            ? [this.manifestHint.index, ...(this.manifestHint.packages || [])]
            : (this.task?.files || []);
        const offsets = this.manifestHint ? {} : this.task?.offsets;
        return expected.filter(file => file?.name &&
            (offsets?.[file.name] || 0) !== file.size &&
            this.chosen.get(file.name)?.size !== file.size).map(file => file.name);
    },

    get uploadFiles() {
        if (this.manifest) return [];
        const show = this.task?.status === 'receiving' || this.task?.phase === 'downloading' || (this.canRetry && this.task?.remote_source);
        if (!show) return [];
        return this.task.files || [];
    },

    get showingRemoteProgress() {
        return this.task?.phase === 'downloading' || (this.sourceMode === 'remote' && Boolean(this.task?.remote_source));
    },

    get uploadProgress() {
        return this.uploadFiles.reduce((progress, file) => {
            progress.received += this.received(file);
            progress.total += file.size;
            return progress;
        }, {received: 0, total: 0});
    },

    received(file) {
        if (this.task?.remote_download?.name === file.name) return this.task.remote_download.received;
        return this.task?.offsets?.[file.name] || 0;
    },

    uploadPercent(received, total) {
        return (total > 0 ? Math.floor(received / total * 100) : 0) + '%';
    },

    uploadStatus(file) {
        if (this.showingRemoteProgress) {
            if (this.task.remote_download?.name === file.name) {
                return t(this.received(file) === file.size ? 'portable_import.part_verifying' : 'portable_import.part_downloading');
            }
            return t(this.received(file) === file.size ? 'portable_import.part_downloaded' : 'portable_import.part_download_pending');
        }
        if (this.received(file) === file.size) return t('portable_import.part_uploaded');
        if (this.uploadingFile === file.name) return t('portable_import.part_uploading');
        if (this.received(file) > 0) return t('portable_import.part_resume');
        return t('portable_import.part_pending');
    },

    get siteLink() {
        return (document.body.dataset.panelPrefix || '') + '/websites/' + this.task?.site_id;
    },

    get statusText() {
        if (this.task?.status === 'receiving' && !this.task.files?.length) return t('portable_import.expired');
        return this.statusFor(this.task);
    },

    get sqlProgress() { return this.task?.sql_progress; },

    get canDeleteTask() {
        return Boolean(this.task && this.task.status !== 'running' && !this.confirmingSubmit);
    },

    get canRetry() {
        return ['failed', 'interrupted'].includes(this.task?.status);
    },

    get failedStage() {
        if (!this.canRetry) return '';
        if (this.task.phase === 'uploading') return t('portable_import.part_uploading');
        return this.statusFor({...this.task, status: 'running'});
    },

    statusFor(task) {
        if (!task) return '';
        const key = task.status === 'running' ? (task.phase === 'files' ? 'files_stage' : task.phase) : task.status;
        return t('portable_import.' + key);
    },

    async request(path = '', method = 'GET', body, contentType, signal) {
        const prefix = document.body.dataset.panelPrefix || '';
        const headers = {'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]')?.content || ''};
        if (contentType) headers['Content-Type'] = contentType;
        const response = await fetch(prefix + '/api/portable-imports' + path, {method, headers, body, signal, cache: 'no-store'});
        if (response.status === 401) {
            location.assign(prefix + '/login');
            throw Object.assign(new Error(t('auth.session_expired')), {status: response.status});
        }
        const result = await response.json().catch(error => {
            if (response.ok) throw error;
            return null;
        });
        if (!response.ok || !result?.success) {
            const error = new Error(result?.message || t('common.operation_failed'));
            error.status = response.status;
            throw error;
        }
        return result.data;
    },

    async refreshList() {
        try { this.tasks = await this.request(); }
        catch (error) { this.error = error.message; }
    },

    async open(id) {
        if (this.working) return;
        clearTimeout(this.timer);
        this.taskView++;
        this.task = this.tasks.find(item => item.id === id && item.status === 'running') || null;
        this.statusUnavailable = false;
        this.confirmingSubmit = false;
        this.logs = null;
        this.error = '';
        this.aliasesDraft = null;
        this.remoteLink = '';
        this.remoteReady = false;
        this.resetSelection();
        await this.refreshTask(id);
        this.sourceMode = this.task?.remote_source ? 'remote' : 'upload';
    },

    resetSelection() {
        this.selectionVersion++;
        this.chosen = new Map();
        this.manifest = null;
        this.manifestHint = null;
        this.bundleSize = 0;
        this.aliasSuggestion = '';
        this.suggestingAlias = false;
    },

    async choose(event) {
        this.error = '';
        const previousDomain = this.domain;
        this.resetSelection();
        const version = this.selectionVersion;
        const selection = Array.from(event.target.files);
        event.target.value = '';
        if (!selection.length) return;
        try {
            const bundle = selection.find(file => file.name.toLowerCase().endsWith('.tar'));
            if (bundle) {
                if (selection.length !== 1) throw new Error(t('portable_import.separate'));
                const header = new TextDecoder().decode(await bundle.slice(0, 512).arrayBuffer());
                if (version !== this.selectionVersion) return;
                const name = header.slice(0, 100).split('\0')[0];
                const encodedSize = header.slice(124, 136).replace(/\0/g, '').trim();
                const size = /^[0-7]+$/.test(encodedSize) ? parseInt(encodedSize, 8) : 0;
                if (name !== 'manifest.json' || size <= 0 || size > 4 * 1024 * 1024) {
                    throw new Error(t('portable_import.bad_manifest'));
                }
                this.chosen.set('bundle.tar', bundle);
                this.manifest = bundle.slice(512, 512 + size);
                this.bundleSize = bundle.size;
            } else {
                this.chosen = new Map(selection.map(file => [file.name, file]));
                this.manifest = this.chosen.get('manifest.json') || null;
                if (!this.manifest && this.task?.status !== 'receiving' && !this.canRetry) throw new Error(t('portable_import.bad_manifest'));
                if (this.manifest?.size > 4 * 1024 * 1024) throw new Error(t('portable_import.bad_manifest'));
            }
            if (this.manifest) {
                const hint = JSON.parse(await this.manifest.text());
                if (version !== this.selectionVersion) return;
                this.manifestHint = hint;
                if (this.manifestHint?.magic !== 'wp-panel-backup-development' || !Array.isArray(this.manifestHint.packages)) {
                    throw new Error(t('portable_import.bad_manifest'));
                }
                if (!this.canRetry && (this.task?.status !== 'receiving' || this.task.domain !== this.domain)) {
                    if (previousDomain !== this.domain) this.aliasesDraft = null;
                    this.suggestingAlias = true;
                    try {
                        const result = await this.request('/alias-suggestion?domain=' + encodeURIComponent(this.domain),
                            'GET', undefined, undefined, AbortSignal.timeout?.(5000));
                        if (version === this.selectionVersion) this.aliasSuggestion = result.alias;
                    } catch {
                        // The optional field stays editable when a suggestion is unavailable.
                    } finally {
                        if (version === this.selectionVersion) this.suggestingAlias = false;
                    }
                }
            }
        } catch (error) {
            if (version !== this.selectionVersion) return;
            this.resetSelection();
            this.error = error.message;
        }
    },

    editSourceIP() {
        this.sourceIP = '';
        this.error = '';
        this.editingSourceIP = true;
        this.$nextTick(() => this.$refs.sourceIP.focus());
    },

    closeSourceIP() {
        if (!this.editingSourceIP || this.working) return;
        this.editingSourceIP = false;
        this.$nextTick(() => this.$refs.editSourceIP.focus());
    },

    async applySourceIP() {
        const ip = this.sourceIP.trim();
        if (ip && await this.previewRemote(ip)) this.closeSourceIP();
    },

    async previewRemote(sourceIP = '') {
        if (this.working || this.confirmingSubmit || this.task?.status === 'running' || !this.remoteLink.trim()) return;
        this.working = true;
        this.remoteReady = false;
        this.error = '';
        try {
            const body = {
                link: this.remoteLink.trim(),
                task_id: this.task?.remote_source ? this.task.id : '',
                source_ip: sourceIP
            };
            const task = await this.request('/remote-preview', 'POST', JSON.stringify(body), 'application/json');
            if (this.task?.id !== task.id) this.aliasesDraft = null;
            this.resetSelection();
            await this.displayTask(task);
            this.remoteReady = true;
            return true;
        } catch (error) {
            this.error = error.message;
        } finally {
            this.working = false;
        }
    },

    async startRemote() {
        if (this.working || this.confirmingSubmit || !this.remoteReady || !this.task?.id || this.task.status === 'running') return;
        const id = this.task.id;
        const aliases = this.aliasesText.split('\n').map(alias => alias.trim()).filter(Boolean);
        const retry = this.canRetry || this.task.phase === 'retry_uploading';
        this.working = true;
        this.error = '';
        try {
            let plan = null;
            if (retry) {
                plan = await this.request('/' + id + '/retry-plan', 'POST', JSON.stringify({aliases}), 'application/json');
                if (plan?.token && !await confirmModal(t('portable_import.confirm_cleanup_retry') + '\n\n' + plan.resources.join('\n'))) return;
            }
            try {
                const body = {link: this.remoteLink.trim(), aliases, cleanup_token: plan?.token};
                await this.request('/' + id + '/remote-submit', 'POST', JSON.stringify(body), 'application/json', AbortSignal.timeout?.(15000));
                this.aliasesDraft = null;
                this.remoteReady = false;
                this.remoteLink = '';
                this.task = {...this.task, status: 'running', phase: 'downloading', message: ''};
            } catch (error) {
                if (error.status >= 400 && error.status < 500 && error.status !== 408) throw error;
                this.confirmingSubmit = true;
            }
            await this.refreshTask(id);
        } catch (error) {
            this.error = error.message;
        } finally {
            this.working = false;
            if (!this.confirmingSubmit) await this.refreshList();
        }
    },

    async upload(retry = false) {
        if (this.working || this.task?.status === 'running' || this.confirmingSubmit || this.suggestingAlias) return;
        const aliases = this.aliasesText.split('\n').map(alias => alias.trim()).filter(Boolean);
        const resume = retry || this.task?.phase === 'retry_uploading';
        const planRetry = () => this.request('/' + this.task.id + '/retry-plan', 'POST', JSON.stringify({aliases}), 'application/json');
        const confirmRetry = plan => !plan?.token || confirmModal(
            t('portable_import.confirm_cleanup_retry') + '\n\n' + plan.resources.join('\n')
        );
        this.working = true;
        this.error = '';
        this.logs = null;
        try {
            let plan = resume ? await planRetry() : null;
            const confirmed = plan?.materials_ready;
            if (confirmed && !await confirmRetry(plan)) return;
            if (retry || (this.task?.phase === 'retry_uploading' && this.manifest)) {
                const query = this.bundleSize ? '?bundle_size=' + this.bundleSize : '';
                this.task = await this.request('/' + this.task.id + '/retry' + query, 'POST', this.manifest || undefined, 'application/json');
                this.manifest = null;
                this.manifestHint = null;
                this.bundleSize = 0;
            } else if (this.manifest) {
                const query = this.bundleSize ? '?bundle_size=' + this.bundleSize : '';
                this.task = await this.request(query, 'POST', this.manifest, 'application/json');
                this.manifest = null;
                this.manifestHint = null;
                this.bundleSize = 0;
            } else if (this.task?.id) {
                this.task = await this.request('/' + this.task.id);
            }
            if (!this.task) return;
            if (this.task.status !== 'receiving') {
                this.resetSelection();
                await this.displayTask(this.task);
                return;
            }
            if (!this.task.files?.length) throw new Error(t('portable_import.expired'));
            const id = this.task.id;
            const saved = await this.request('/' + id + '/aliases', 'PUT', JSON.stringify({aliases}), 'application/json');
            this.task.aliases = saved.aliases;
            history.replaceState(null, '', '#' + id);
            if (resume && this.missing.length && !this.chosen.size) return;
            let uploaded = false;
            for (const file of this.task.files) {
                let offset = this.received(file);
                const selected = this.chosen.get(file.name);
                if (offset >= file.size || selected?.size !== file.size) continue;
                uploaded = true;
                this.uploadingFile = file.name;
                while (offset < file.size) {
                    const data = selected.slice(offset, offset + 5 * 1024 * 1024);
                    const endpoint = '/' + id + '/files/' + encodeURIComponent(file.name) + '?offset=' + offset;
                    const result = await this.uploadChunk(endpoint, data);
                    offset = result.offset;
                    this.task.offsets[file.name] = offset;
                }
            }
            this.uploadingFile = '';
            if (this.task.files.some(file => this.task.offsets?.[file.name] !== file.size)) {
                throw new Error(t('portable_import.incomplete'));
            }
            if (resume && (uploaded || !confirmed)) {
                plan = await planRetry();
                if (!await confirmRetry(plan)) return;
            }
            try {
                await this.request('/' + id + '/submit', 'POST', JSON.stringify({aliases, cleanup_token: plan?.token}), 'application/json', AbortSignal.timeout?.(15000));
                this.aliasesDraft = null;
                this.task = {...this.task, status: 'running', phase: 'validating', message: ''};
                this.resetSelection();
            } catch (error) {
                if (error.status >= 400 && error.status < 500 && error.status !== 408) throw error;
                this.confirmingSubmit = true;
            }
            await this.refreshTask(id);
        } catch (error) {
            this.error = error.message;
        } finally {
            this.uploadingFile = '';
            this.working = false;
            if (!this.confirmingSubmit) await this.refreshList();
        }
    },

    async uploadChunk(endpoint, body) {
        for (let attempt = 0; ; attempt++) {
            try {
                return await this.request(endpoint, 'PUT', body, 'application/octet-stream');
            } catch (error) {
                if (!(error instanceof TypeError) || attempt >= 2) throw error;
                await new Promise(resolve => setTimeout(resolve, 500 * (attempt + 1)));
            }
        }
    },

    async refreshTask(id, view = this.taskView) {
        let task;
        try {
            task = await this.request('/' + id, 'GET', undefined, undefined, AbortSignal.timeout?.(15000));
        } catch (error) {
            if (view !== this.taskView) return;
            if (error.status === 404) {
                this.clearTask();
                await this.refreshList();
            } else if (this.confirmingSubmit || (this.task?.id === id && this.task.status === 'running')) {
                this.statusUnavailable = !this.confirmingSubmit;
                this.timer = setTimeout(() => this.refreshTask(id, view), 3000);
                return;
            }
            this.error = error.message;
            return;
        }
        if (view !== this.taskView) return;
        await this.displayTask(task);
    },

    async displayTask(task) {
        const confirmingSubmit = this.confirmingSubmit;
        this.task = task;
        this.statusUnavailable = false;
        this.confirmingSubmit = false;
        history.replaceState(null, '', '#' + task.id);
        if (confirmingSubmit) {
            if (task.status === 'receiving' && task.files?.length) {
                this.error = t('portable_import.submit_not_started');
            } else {
                this.aliasesDraft = null;
                this.resetSelection();
            }
        }
        if (task.status === 'running') {
            this.remoteLink = '';
            this.remoteReady = false;
            const view = this.taskView;
            this.timer = setTimeout(() => this.refreshTask(task.id, view), 1500);
            if (!confirmingSubmit) return;
        }
        await this.refreshList();
    },

    clearTask() {
        clearTimeout(this.timer);
        this.taskView++;
        this.task = null;
        this.remoteLink = '';
        this.remoteReady = false;
        this.logs = null;
        this.statusUnavailable = false;
        this.confirmingSubmit = false;
        this.aliasesDraft = null;
        this.resetSelection();
        history.replaceState(null, '', location.pathname + location.search);
    },

    async showLog() {
        try { this.logs = await this.request('/' + this.task.id + '?log=php'); }
        catch (error) { this.error = error.message; }
    },

    async remove() {
        const task = this.task;
        if (this.working || !this.canDeleteTask) return;
        const message = task.resources && task.status !== 'succeeded'
            ? 'confirm_delete_task_resources'
            : 'confirm_delete_task';
        if (!await confirmModal(t('portable_import.' + message)) || this.task?.id !== task.id || this.working) return;
        this.working = true;
        this.error = '';
        try {
            await this.request('/' + task.id + '/record', 'DELETE');
            this.clearTask();
            await this.refreshList();
        } catch (error) { this.error = error.message; }
        finally { this.working = false; }
    }
})));
