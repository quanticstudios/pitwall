'use strict';
// invariant: a push holds what the page shows, never a pane's screen.

self.addEventListener('push', (e) => {
  let n = {};
  try { n = e.data ? e.data.json() : {}; } catch (err) { /* shown as a bare notice */ }
  e.waitUntil(self.registration.showNotification(n.title || 'pitwall', {
    body: n.body || '',
    tag: n.pane || 'pitwall', // a newer push about a pane replaces the older one
    data: { pane: n.pane || '' },
    icon: 'icon-192.png',
  }));
});

// A tap opens the page on the pane the push is about.
self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const pane = (e.notification.data && e.notification.data.pane) || '';
  e.waitUntil((async () => {
    const open = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const c of open) {
      if ('focus' in c) {
        await c.focus();
        c.postMessage({ pane });
        return;
      }
    }
    await self.clients.openWindow(pane ? '/#pane=' + encodeURIComponent(pane) : '/');
  })());
});
