(() => {
    'use strict';

    const dialog = document.getElementById('wpp-mail-preview-dialog');
    if (!dialog) return;

    const cleanUrl = () => {
        const url = new URL(window.location.href);
        url.searchParams.delete('view');
        url.searchParams.delete('_wpnonce');
        window.history.replaceState({}, '', url.toString());
    };
    const returnFocus = () => {
        const messageId = dialog.dataset.messageId;
        const trigger = messageId
            ? Array.from(document.querySelectorAll('[data-wpp-mail-preview-id]')).find((link) => link.dataset.wppMailPreviewId === messageId)
            : null;
        if (trigger) trigger.focus();
    };
    const close = () => {
        if (typeof dialog.close === 'function') {
            dialog.close();
        } else {
            dialog.removeAttribute('open');
            cleanUrl();
            returnFocus();
        }
    };
    dialog.querySelectorAll('[data-wpp-mail-preview-close]').forEach((control) => {
        control.addEventListener('click', (event) => {
            event.preventDefault();
            close();
        });
    });
    dialog.addEventListener('click', (event) => {
        if (event.target !== dialog) return;
        const bounds = dialog.getBoundingClientRect();
        const outside = event.clientX < bounds.left || event.clientX > bounds.right
            || event.clientY < bounds.top || event.clientY > bounds.bottom;
        if (outside) close();
    });
    dialog.addEventListener('close', () => {
        cleanUrl();
        returnFocus();
    });

    if (typeof dialog.showModal === 'function') {
        dialog.removeAttribute('open');
        dialog.showModal();
    }
})();
