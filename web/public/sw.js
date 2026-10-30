/*
 * Zafiro service worker.
 * - Pages are never cached: they hold live club data, CSRF tokens and differ per user
 *   and club. Without a network a navigation gets the offline page instead.
 * - Built assets (/build/*, file names change with every build) are cache-first.
 * - Icons, images and fonts are served from cache and refreshed in the background.
 * - API calls, form posts and everything else go straight to the network.
 */
const VERSION = 'v2';
const OFFLINE_URL = '/offline';
const PAGES = `zafiro-pages-${VERSION}`;
const ASSETS = 'zafiro-assets';
const STATIC = 'zafiro-static';
const MAX_ASSETS = 60;

self.addEventListener('install', (event) => {
    event.waitUntil(caches.open(PAGES).then((cache) => cache.addAll([OFFLINE_URL, '/icons/icon-192.png'])).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (event) => {
    event.waitUntil(
        caches.keys()
            .then((keys) => Promise.all(keys.filter((key) => key.startsWith('zafiro-pages-') && key !== PAGES).map((key) => caches.delete(key))))
            .then(() => self.clients.claim()),
    );
});

const trim = async (cacheName, max) => {
    const cache = await caches.open(cacheName);
    const keys = await cache.keys();
    await Promise.all(keys.slice(0, Math.max(0, keys.length - max)).map((key) => cache.delete(key)));
};

const cacheFirst = async (request) => {
    const cached = await caches.match(request);
    if (cached) return cached;
    const response = await fetch(request);
    if (response.ok) {
        const cache = await caches.open(ASSETS);
        await cache.put(request, response.clone());
        trim(ASSETS, MAX_ASSETS);
    }
    return response;
};

const staleWhileRevalidate = async (event, request) => {
    const cache = await caches.open(STATIC);
    const cached = await caches.match(request); // any cache: the offline page's icon is precached
    const network = fetch(request).then((response) => {
        if (response.ok || response.type === 'opaque') cache.put(request, response.clone());
        return response;
    });
    if (cached) {
        event.waitUntil(network.catch(() => {}));
        return cached;
    }
    return network;
};

self.addEventListener('fetch', (event) => {
    const request = event.request;
    if (request.method !== 'GET') return;
    const url = new URL(request.url);

    if (request.mode === 'navigate') {
        event.respondWith(fetch(request).catch(() => caches.match(OFFLINE_URL)));
        return;
    }

    if (url.origin === self.location.origin) {
        if (url.pathname.startsWith('/build/')) event.respondWith(cacheFirst(request));
        else if (/^\/(icons|images)\//.test(url.pathname)) event.respondWith(staleWhileRevalidate(event, request));
        return;
    }

    if (['cdnjs.cloudflare.com', 'fonts.googleapis.com', 'fonts.gstatic.com'].includes(url.hostname)) {
        event.respondWith(staleWhileRevalidate(event, request));
    }
});
