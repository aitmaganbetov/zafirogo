/*
 * Zafiro service worker (перенос public/sw.js старого проекта под сборку Vite; решение Q-068).
 * - Страницы не кэшируются (как в старом): index.html ссылается на ассеты текущей сборки, данные клуба живые.
 *   Без сети переход (navigate) получает офлайн-страницу /offline (её HTML с текстами на всех языках отдаёт сервер).
 * - Ассеты сборки Vite (/assets/*: JS, CSS, шрифты из npm — имя меняется с каждой сборкой) — сначала из кэша.
 * - Иконки и картинки (/icons/*, /images/*) — из кэша с обновлением в фоне.
 * - API (/api/*), WebSocket (/ws), отправка форм и всё остальное — сразу в сеть.
 * - Внешних источников нет (шрифты и иконки — из npm в сборке, Q-064), поэтому правил для CDN нет.
 */
const VERSION = 'v3';
const OFFLINE_URL = '/offline';
const PAGES = `zafiro-pages-${VERSION}`;
const ASSETS = `zafiro-assets-${VERSION}`;
const STATIC = 'zafiro-static';
// Сборка Vite делится на чанки по страницам: держим больше файлов, чем 60 у старой (/build/* — несколько файлов).
const MAX_ASSETS = 200;
const KEEP = [PAGES, ASSETS, STATIC];

self.addEventListener('install', (event) => {
    event.waitUntil(caches.open(PAGES).then((cache) => cache.addAll([OFFLINE_URL, '/icons/icon-192.png'])).then(() => self.skipWaiting()));
});

// Удаляем кэши прежних версий, в том числе старого Laravel (zafiro-pages-v2, zafiro-assets с /build/*).
self.addEventListener('activate', (event) => {
    event.waitUntil(
        caches.keys()
            .then((keys) => Promise.all(keys.filter((key) => key.startsWith('zafiro-') && !KEEP.includes(key)).map((key) => caches.delete(key))))
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
    const cached = await caches.match(request); // любой кэш: иконка офлайн-страницы закэширована при установке
    const network = fetch(request).then((response) => {
        if (response.ok) cache.put(request, response.clone());
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
    if (url.origin !== self.location.origin) return;

    if (request.mode === 'navigate') {
        event.respondWith(fetch(request).catch(() => caches.match(OFFLINE_URL)));
        return;
    }

    if (url.pathname.startsWith('/assets/')) event.respondWith(cacheFirst(request));
    else if (/^\/(icons|images)\//.test(url.pathname)) event.respondWith(staleWhileRevalidate(event, request));
});
