// Notifications only: no fetch handler, offline cache, or payment requests.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const page = windows.find((client) => new URL(client.url).pathname === "/");
    if (page) return page.focus();
    return self.clients.openWindow("/");
  })());
});
