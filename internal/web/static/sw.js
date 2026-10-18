// ffagent's service worker. It only shows notifications the agent pushes;
// it doesn't cache anything, so the app always loads fresh from the server.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("push", (e) => {
  let n = {};
  try { n = e.data ? e.data.json() : {}; } catch (_) { n = { body: e.data && e.data.text() }; }
  const opts = {
    body: n.body || "",
    icon: "icon-192.png",
    badge: "icon-192.png",
    data: { url: n.url || "./" },
  };
  if (n.tag) { opts.tag = n.tag; opts.renotify = true; }
  e.waitUntil(self.registration.showNotification(n.title || "ffagent", opts));
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const url = new URL((e.notification.data && e.notification.data.url) || "./", self.registration.scope).href;
  e.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const w of wins) {
      if (w.url.startsWith(self.registration.scope)) {
        await w.focus();
        if (w.url !== url && "navigate" in w) await w.navigate(url).catch(() => {});
        return;
      }
    }
    await self.clients.openWindow(url);
  })());
});
