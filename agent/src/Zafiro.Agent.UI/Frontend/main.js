// Лаунчер гостя (перенос WindowsAgent/Frontend/main.js старого агента 1:1 по виду). Вызовы — мост к службе
// (window.ZafiroApp.bridge → ipc.proto → gRPC сервера), данные — в формате нового контракта: суммы — целые тиыны
// (строки int64 → Zafiro.int64), ошибки — текст сервера (LocalizedMessage) или запасной ключ. Сессия, гость, баланс,
// настройки клуба — из снимка службы (событие snapshot), а не из опросов: опросов нет, всё приходит push.
(() => {
  'use strict';
  const Z = window.Zafiro;
  const app = window.ZafiroApp;
  const { i18n, bridge } = app;
  let products = [];

  // Тексты интерфейса: ключ — русский текст; строки сервера (agent.GetTranslations через службу) поверх встроенных.
  const languageLabels = { ru: 'RU', kk: 'ҚАЗ', en: 'EN' };
  const intlLocale = () => i18n.intlLocale();
  const t = i18n.t;
  const tc = i18n.tc;

  // Картинки клуба (новости, экран входа) страница берёт с виртуального хоста img.zafiro.invalid: их отдаёт процесс UI
  // из кэша службы (AG-13), у страницы нет сети. У товаров картинок нет и в старом (ProductResource: колонки нет) —
  // заглушка, как в старом агенте.
  const placeholderImage = 'product-placeholder.svg';
  const imageHost = 'https://img.zafiro.invalid/';
  const normalizeProduct = (product) => ({
    id: Z.int64(product.productId) || null,
    name: product.name || null,
    description: product.category || product.sku || null,
    // Категория склада клуба как есть (данные клуба, не переводятся).
    category: typeof product.category === 'string' && product.category.trim() ? product.category.trim() : null,
    price: Z.int64(product.priceTiyn),
    currency: product.currency || 'KZT',
    quantity: Number(product.quantity ?? 0),
    inStock: Boolean(product.inStock),
    image: placeholderImage
  });
  const productName = (product) => product.name ?? t('Товар');

  const cart = new Map();
  let shopPaying = false;
  let cartTotalPrice = 0;
  const shopCatalog = document.getElementById('shopCatalog');
  const sessionPage = document.getElementById('sessionPage');
  const shopPage = document.getElementById('shopPage');
  const historyPage = document.getElementById('historyPage');
  const tariffPage = document.getElementById('tariffPage');
  const profilePage = document.getElementById('profilePage');
  const friendsPage = document.getElementById('friendsPage');
  const chatPage = document.getElementById('chatPage');
  const tariffCatalog = document.getElementById('tariffCatalog');
  const tariffPageStatus = document.getElementById('tariffPageStatus');
  const pageToggle = document.getElementById('pageToggle');
  const homeNavigation = document.getElementById('homeNavigation');
  const shopNavigation = document.getElementById('shopNavigation');
  const historyNavigation = document.getElementById('historyNavigation');
  const tariffNavigation = document.getElementById('tariffNavigation');
  const profileNavigation = document.getElementById('profileNavigation');
  const friendsNavigation = document.getElementById('friendsNavigation');
  const chatNavigation = document.getElementById('chatNavigation');
  const cartDialog = document.getElementById('cartDialog');
  const actionStatus = document.getElementById('actionStatus');
  const shopCount = document.getElementById('shopCount');
  // Мост к службе есть (страница открыта в UI агента, а не в обычном браузере).
  const webview = bridge.available;
  let activeCategory = 'all';
  let currentView = 'session';
  let lastTariffs = null;
  let productsFailed = false;
  let userName = null; // null — гость
  let sessionStartedAt = null;

  if (!shopCatalog) return;

  // Сообщения, показанные по ключу, при смене языка показываются заново; текст сервера — как есть.
  const statusKeys = new Map();
  const setStatus = (element, key, params = {}) => {
    statusKeys.set(element, [key, params]);
    element.textContent = key ? t(key, params) : '';
  };
  const setServerStatus = (element, text) => {
    statusKeys.delete(element);
    element.textContent = text;
  };

  // Ошибки и предупреждения — всплывающим сообщением: красное (error), жёлтое (warning),
  // зелёное (success), синее (info). Одно и то же сообщение — не чаще раза в 45 с (heartbeat).
  const toasts = document.getElementById('toasts');
  const toastShown = new Map();
  const toast = (text, level = 'error') => {
    if (!text) return;
    const now = Date.now();
    if (now - (toastShown.get(text) ?? 0) < 45000) return;
    toastShown.set(text, now);
    const item = document.createElement('div');
    item.className = `toast ${level}`;
    item.setAttribute('role', level === 'error' || level === 'warning' ? 'alert' : 'status');
    const message = document.createElement('p');
    message.textContent = text;
    const close = document.createElement('button');
    close.type = 'button';
    close.className = 'toast-close';
    close.textContent = '×';
    close.setAttribute('aria-label', t('Закрыть'));
    const remove = () => item.remove();
    close.addEventListener('click', remove);
    item.append(message, close);
    toasts.append(item);
    while (toasts.children.length > 4) toasts.firstElementChild.remove();
    setTimeout(remove, level === 'error' ? 8000 : 5000);
  };
  const toastKey = (key, level = 'error', params = {}) => toast(t(key, params), level);

  // Подтверждение действия в окне агента (не системное окно браузера).
  const confirmDialog = document.getElementById('confirmDialog');
  let confirmResolve = null;
  const askConfirm = (title, text, okLabel) => new Promise((resolve) => {
    if (confirmResolve) confirmResolve(false);
    confirmResolve = resolve;
    document.getElementById('confirmTitle').textContent = title;
    document.getElementById('confirmText').textContent = text;
    document.getElementById('confirmOk').textContent = okLabel;
    if (typeof confirmDialog.showModal === 'function') confirmDialog.showModal();
    else confirmDialog.setAttribute('open', '');
    document.getElementById('confirmCancel').focus();
  });
  const finishConfirm = (answer) => {
    const resolve = confirmResolve;
    confirmResolve = null;
    if (typeof confirmDialog.close === 'function') confirmDialog.close();
    else confirmDialog.removeAttribute('open');
    resolve?.(answer);
  };
  document.getElementById('confirmOk').addEventListener('click', () => finishConfirm(true));
  document.getElementById('confirmCancel').addEventListener('click', () => finishConfirm(false));
  confirmDialog.addEventListener('cancel', (event) => {
    event.preventDefault();
    finishConfirm(false);
  });
  confirmDialog.addEventListener('click', (event) => {
    if (event.target === confirmDialog) finishConfirm(false);
  });

  // Оплата времени: с основного баланса или бонусами. Смешанная оплата в клубе — бонусы до процента
  // клуба, остальное с основного; без неё — бонусами вся цена. Ответ — 'main', 'bonus' или null
  // (отмена). Бонусами нельзя — без вопроса, с основного.
  const payDialog = document.getElementById('payDialog');
  let payResolve = null;
  const askBalance = (title, price, bonusPercent, mixed) => new Promise((resolve) => {
    if (!(bonusPercent > 0)) {
      resolve('main');
      return;
    }
    if (payResolve) payResolve(null);
    payResolve = resolve;
    document.getElementById('payTitle').textContent = title;
    document.getElementById('payText').textContent = mixed
      ? t('К оплате :price. Можно оплатить смешанно: бонусами до :percent% цены, остальное — с основного баланса.', { price, percent: bonusPercent })
      : t('К оплате :price. Оплата целиком с одного баланса: основного или бонусного.', { price });
    document.getElementById('payBonus').textContent = mixed
      ? t('Смешанно: бонусы до :percent% + основной', { percent: bonusPercent })
      : t('С бонусного баланса');
    if (typeof payDialog.showModal === 'function') payDialog.showModal();
    else payDialog.setAttribute('open', '');
    document.getElementById('payMain').focus();
  });
  const finishPay = (answer) => {
    const resolve = payResolve;
    payResolve = null;
    if (typeof payDialog.close === 'function') payDialog.close();
    else payDialog.removeAttribute('open');
    resolve?.(answer);
  };
  document.getElementById('payMain').addEventListener('click', () => finishPay('main'));
  document.getElementById('payBonus').addEventListener('click', () => finishPay('bonus'));
  document.getElementById('payCancel').addEventListener('click', () => finishPay(null));
  payDialog.addEventListener('cancel', (event) => {
    event.preventDefault();
    finishPay(null);
  });
  payDialog.addEventListener('click', (event) => {
    if (event.target === payDialog) finishPay(null);
  });

  const fetchProducts = async () => {
    setStatus(shopCount, 'Загружаем товары…');
    if (!webview) {
      setStatus(shopCount, 'Запустите приложение, чтобы загрузить товары.');
      return;
    }
    try {
      const list = [];
      let pageToken = '';
      for (let page = 0; page < 20; page++) {
        const result = await bridge.call('ShopService/ListProducts', { pageToken });
        list.push(...(Array.isArray(result.products) ? result.products : []));
        pageToken = result.nextPageToken || '';
        if (!pageToken) break;
      }
      // Без id два товара с одинаковым названием попали бы в одну строку корзины.
      products = list.map(normalizeProduct).filter((product) => product.id != null);
      productsFailed = false;
    } catch {
      products = [];
      productsFailed = true;
    }
    renderCategories();
    renderCart();
  };

  // Суммы — целые тиыны; показ — в тенге (Zafiro.formatPrice, без float).
  const formatPrice = (tiyn, currency = 'KZT') => Z.formatPrice(tiyn, intlLocale(), currency);
  const formatDateTime = (value) => new Intl.DateTimeFormat(intlLocale(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));

  // Часы сервера минус часы ПК: оставшееся время не зависит от неверных часов ПК.
  let clockOffset = 0;
  const serverNow = () => Date.now() + clockOffset;

  let balances = [null, null];
  let onBalances = () => {};
  const formatBalance = (value) => value === null ? '—' : Z.formatAmount(value ?? 0, intlLocale());
  // Балансы (тиыны) из Balance контракта (main_tiyn, bonus_tiyn).
  const applyBalance = (balance) => {
    if (balance) applyBalances(Z.int64(balance.mainTiyn), Z.int64(balance.bonusTiyn));
  };
  const balanceSource = (choice) => choice === 'bonus' ? 'BALANCE_SOURCE_BONUS' : 'BALANCE_SOURCE_MAIN';
  const applyBalances = (balance, bonus) => {
    balances = [balance, bonus];
    onBalances();
    const text = { balanceBig: balance, historyBalance: balance, profileBalance: balance, bonusSmall: bonus, historyBonus: bonus, profileBonus: bonus };
    Object.entries(text).forEach(([id, value]) => {
      document.getElementById(id).textContent = formatBalance(value);
    });
  };

  // Магазин в агенте клуб может выключить (настройки клуба).
  let shopEnabled = true;
  const setView = (view) => {
    if (view === 'shop' && !shopEnabled) view = 'session';
    currentView = view;
    const showShop = view === 'shop';
    const showHistory = view === 'history';
    const showTariffs = view === 'tariffs';
    const showProfile = view === 'profile';
    const showFriends = view === 'friends';
    const showChat = view === 'chat';
    sessionPage.hidden = showShop || showHistory || showTariffs || showProfile || showFriends || showChat;
    chatPage.hidden = !showChat;
    chatNavigation.setAttribute('aria-current', showChat ? 'page' : 'false');
    friendsPage.hidden = !showFriends;
    friendsNavigation.setAttribute('aria-current', showFriends ? 'page' : 'false');
    shopPage.hidden = !showShop;
    historyPage.hidden = !showHistory;
    tariffPage.hidden = !showTariffs;
    profilePage.hidden = !showProfile;
    shopNavigation.hidden = !shopEnabled;
    pageToggle.hidden = !shopEnabled && view === 'session';
    pageToggle.querySelector('span').textContent = view === 'session' ? t('Магазин') : t('К сессии');
    pageToggle.setAttribute('aria-label', view === 'session' ? t('Открыть магазин') : t('Вернуться к сессии'));
    homeNavigation.setAttribute('aria-current', view === 'session' ? 'page' : 'false');
    shopNavigation.setAttribute('aria-current', showShop ? 'page' : 'false');
    historyNavigation.setAttribute('aria-current', showHistory ? 'page' : 'false');
    tariffNavigation.setAttribute('aria-current', showTariffs ? 'page' : 'false');
    profileNavigation.setAttribute('aria-current', showProfile ? 'page' : 'false');
  };

  const createActionButton = (label, className, action, accessibleLabel = label) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = className;
    button.textContent = label;
    button.setAttribute('aria-label', accessibleLabel);
    button.addEventListener('click', action);
    return button;
  };

  // Кнопки категорий: «Все товары» и категории, которые есть у товаров клуба.
  const renderCategories = () => {
    const box = document.getElementById('shopCategories');
    const names = [...new Set(products.map((product) => product.category).filter(Boolean))]
      .sort((a, b) => a.localeCompare(b, intlLocale()));
    if (activeCategory !== 'all' && !names.includes(activeCategory)) activeCategory = 'all';
    const all = box.querySelector('[data-category="all"]');
    box.replaceChildren(all, ...names.map((name) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'category-button';
      button.dataset.category = name;
      button.textContent = name;
      return button;
    }));
    box.querySelectorAll('.category-button').forEach((item) => {
      const isActive = item.dataset.category === activeCategory;
      item.classList.toggle('is-active', isActive);
      item.setAttribute('aria-pressed', String(isActive));
    });
  };

  const renderProducts = () => {
    const visibleProducts = activeCategory === 'all'
      ? products
      : products.filter((product) => product.category === activeCategory);

    shopCatalog.replaceChildren();
    if (visibleProducts.length) setServerStatus(shopCount, tc(':count товар|:count товара|:count товаров', visibleProducts.length));
    else setStatus(shopCount, productsFailed && !products.length ? 'Не удалось загрузить товары. Проверьте подключение.' : 'Товары не загружены');

    visibleProducts.forEach((product) => {
      const card = document.createElement('article');
      card.className = 'shop-product';

      const cover = document.createElement('div');
      cover.className = 'shop-product-cover';

      const image = document.createElement('img');
      image.src = product.image;
      image.alt = productName(product);
      image.loading = 'lazy';
      image.addEventListener('error', () => {
        if (!image.src.endsWith(placeholderImage)) image.src = placeholderImage;
      }, { once: true });

      const details = document.createElement('div');
      details.className = 'shop-product-details';

      const name = document.createElement('h3');
      name.textContent = productName(product);

      const description = document.createElement('p');
      description.textContent = product.description ?? t('Товар');

      // Остаток на складе клуба: больше, чем есть, в корзину не добавить.
      const stock = document.createElement('p');
      stock.className = `shop-stock${product.quantity > 0 ? '' : ' empty'}`;
      stock.textContent = product.quantity > 0 ? t('В наличии: :count шт.', { count: product.quantity }) : t('Нет в наличии');

      const footer = document.createElement('div');
      footer.className = 'shop-product-footer';

      const price = document.createElement('strong');
      price.textContent = formatPrice(product.price, product.currency);

      const quantity = cart.get(product.id) || 0;
      const addButton = createActionButton(
        quantity ? t('В корзине · :count', { count: quantity }) : t('В корзину'),
        'shop-buy',
        () => changeQuantity(product.id, 1),
        t('Добавить :product в корзину', { product: productName(product) })
      );
      addButton.disabled = quantity >= product.quantity;
      footer.append(price, addButton);
      details.append(name, description, stock, footer);
      cover.append(image);
      card.append(cover, details);
      shopCatalog.append(card);
    });
  };

  const renderCartItems = (container, emptyElement) => {
    container.replaceChildren();
    const entries = [...cart.entries()];
    emptyElement.hidden = entries.length > 0;

    entries.forEach(([productId, quantity]) => {
      const product = products.find((item) => item.id === productId);
      if (!product) return;

      const row = document.createElement('div');
      row.className = 'cart-item';

      const info = document.createElement('div');
      info.className = 'cart-item-info';

      const name = document.createElement('strong');
      name.textContent = productName(product);

      const price = document.createElement('span');
      price.textContent = formatPrice(product.price * quantity);
      info.append(name, price);

      const controls = document.createElement('div');
      controls.className = 'quantity-controls';
      controls.append(
        createActionButton('−', 'quantity-button', () => changeQuantity(productId, -1), t('Уменьшить количество: :product', { product: productName(product) })),
        Object.assign(document.createElement('span'), { textContent: String(quantity) }),
        createActionButton('+', 'quantity-button', () => changeQuantity(productId, 1), t('Увеличить количество: :product', { product: productName(product) }))
      );
      row.append(info, controls);
      container.append(row);
    });
  };

  const renderCart = () => {
    const totalQuantity = [...cart.values()].reduce((sum, quantity) => sum + quantity, 0);
    const totalPrice = [...cart.entries()].reduce((sum, [productId, quantity]) => {
      const product = products.find((item) => item.id === productId);
      return sum + (product ? product.price * quantity : 0);
    }, 0);

    document.getElementById('cartCount').textContent = String(totalQuantity);
    document.getElementById('cartPanelCount').textContent = String(totalQuantity);
    document.getElementById('cartTotal').textContent = formatPrice(totalPrice);
    document.getElementById('dialogCartTotal').textContent = formatPrice(totalPrice);
    document.querySelectorAll('[data-cart-pay]').forEach((button) => {
      button.disabled = totalQuantity === 0 || shopPaying;
    });
    cartTotalPrice = totalPrice;
    renderCartItems(document.getElementById('cartItems'), document.getElementById('cartEmpty'));
    renderCartItems(document.getElementById('dialogCartItems'), document.getElementById('dialogCartEmpty'));
    renderProducts();
  };

  function changeQuantity(productId, change) {
    const product = products.find((item) => item.id === productId);
    const nextQuantity = Math.min((cart.get(productId) || 0) + change, product ? product.quantity : Infinity);
    if (nextQuantity > 0) cart.set(productId, nextQuantity);
    else cart.delete(productId);
    renderCart();
  }

  const tariffValue = (tariff, property) => tariff[property] ?? tariff[property[0].toUpperCase() + property.slice(1)];
  const formatTariffDuration = (minutes) => {
    const value = Number(minutes) || 0;
    if (value < 60 || value % 60 !== 0) return t(':count мин', { count: value });
    return tc(':count час|:count часа|:count часов', value / 60);
  };

  const renderTariffs = (tariffs) => {
    lastTariffs = tariffs;
    tariffCatalog.replaceChildren();

    if (!tariffs.length) {
      setStatus(tariffPageStatus, 'Сейчас тарифы недоступны.');
      return;
    }

    setStatus(tariffPageStatus, null);
    tariffs.forEach((tariff) => {
      const card = document.createElement('article');
      card.className = 'tariff-card';

      const duration = document.createElement('span');
      duration.className = 'tariff-duration';
      duration.textContent = formatTariffDuration(tariffValue(tariff, 'minutes'));

      const name = document.createElement('h2');
      name.textContent = tariffValue(tariff, 'name') || t('Игровой тариф');

      const description = document.createElement('p');
      description.textContent = tariffValue(tariff, 'description') || t(':duration игры', { duration: formatTariffDuration(tariffValue(tariff, 'minutes')) });

      const footer = document.createElement('div');
      footer.className = 'tariff-card-footer';

      const price = document.createElement('strong');
      price.textContent = formatPrice(Z.int64(tariff.priceTiyn), tariff.currency || 'KZT');

      const button = document.createElement('button');
      button.className = 'tariff-purchase';
      button.type = 'button';
      button.textContent = t('Выбрать');
      button.addEventListener('click', async () => {
        const tariffId = Z.int64(tariff.tariffId);
        if (!tariffId || !webview) {
          toastKey('Покупка доступна в приложении.', 'warning');
          return;
        }

        const balance = await askBalance(name.textContent, price.textContent, Number(tariff.bonusMaxPercent) || 0, tariff.mixedPayment === true);
        if (!balance) return;
        setStatus(tariffPageStatus, 'Оформляем тариф…');
        tariffCatalog.querySelectorAll('.tariff-purchase').forEach((item) => {
          item.disabled = true;
        });
        purchaseTime('GuestService/PurchaseTariff', { tariffId: String(tariffId), balanceSource: balanceSource(balance) });
      });

      footer.append(price, button);
      card.append(duration, name, description, footer);
      tariffCatalog.append(card);
    });
  };

  document.getElementById('shopCategories').addEventListener('click', (event) => {
    const button = event.target.closest('[data-category]');
    if (!button) return;

    activeCategory = button.dataset.category;
    document.querySelectorAll('.category-button').forEach((item) => {
      const isActive = item === button;
      item.classList.toggle('is-active', isActive);
      item.setAttribute('aria-pressed', String(isActive));
    });
    renderProducts();
  });

  document.getElementById('cartToggle').addEventListener('click', () => {
    if (typeof cartDialog.showModal === 'function') cartDialog.showModal();
    else cartDialog.setAttribute('open', '');
  });
  document.getElementById('cartClose').addEventListener('click', () => cartDialog.close());
  document.getElementById('clearCart').addEventListener('click', () => {
    cart.clear();
    renderCart();
  });

  // Оплата заказа с основного счёта: сервер списывает деньги и товар, кассир получает заказ в чат.
  document.querySelectorAll('[data-cart-pay]').forEach((button) => button.addEventListener('click', async () => {
    if (!webview || cart.size === 0 || shopPaying) return;
    const items = [...cart.entries()].map(([id, quantity]) => ({ id: Number(id), quantity }));
    if (items.some((item) => !Number.isInteger(item.id) || item.id <= 0)) {
      toastKey('Не удалось оформить заказ. Обновите магазин.');
      return;
    }
    const confirmed = await askConfirm(t('Оплатить заказ'), t('С основного счёта будет списано :amount. Сотрудник клуба принесёт заказ к вашему ПК.', { amount: formatPrice(cartTotalPrice) }), t('Оплатить'));
    if (!confirmed) return;
    shopPaying = true;
    renderCart();
    try {
      const result = await bridge.call('ShopService/PlaceOrder', { items: items.map((item) => ({ productId: String(item.id), quantity: item.quantity })) });
      shopPaying = false;
      cart.clear();
      closeDialog(cartDialog);
      applyBalance(result.balance);
      toast(t('Заказ оплачен.'), 'success');
      fetchProducts();
      requestChat();
    } catch (error) {
      shopPaying = false;
      toast(error.text(t, 'Не удалось оформить заказ.'));
    }
    renderCart();
  }));
  cartDialog.addEventListener('click', (event) => {
    if (event.target === cartDialog) cartDialog.close();
  });

  setView('session');
  document.getElementById('btnReturn')?.addEventListener('click', () => {
    bridge.call('ui.ReturnToDesktop').catch(() => {});
  });
  document.getElementById('btnExtend')?.addEventListener('click', () => {
    setView('tariffs');
    requestMinutes();
    requestPackages();
  });
  document.getElementById('btnTopUp')?.addEventListener('click', () => {
    toastKey('Пополнить баланс можно на кассе клуба.', 'info');
  });
  // «Обновить»: баланс с сервера сразу, не дожидаясь автообновления (например, после пополнения на кассе).
  const refreshBalanceButton = document.getElementById('btnRefreshBalance');
  let refreshBalanceTimer = null;
  const finishBalanceRefresh = () => {
    clearTimeout(refreshBalanceTimer);
    refreshBalanceButton.disabled = false;
    refreshBalanceButton.removeAttribute('aria-busy');
  };
  refreshBalanceButton?.addEventListener('click', () => {
    if (!webview) return;
    refreshBalanceButton.disabled = true;
    refreshBalanceButton.setAttribute('aria-busy', 'true');
    refreshBalanceTimer = setTimeout(finishBalanceRefresh, 15000);
    bridge.call('GuestService/GetProfile').then((result) => {
      applyBalance(result.balance);
      finishBalanceRefresh();
      setStatus(actionStatus, 'Баланс обновлён.');
    }).catch(() => {
      finishBalanceRefresh();
      toastKey('Не удалось обновить баланс. Проверьте подключение.');
    });
  });
  const endButton = document.getElementById('btnEnd');
  const endButtonLabel = endButton.querySelector('span');
  const endButtonIcon = document.getElementById('endIconPath');
  const endIconPaths = {
    end: 'M12 3v9m6.4-5.4a8 8 0 1 1-12.8 0',
    logout: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4m7 14 5-5-5-5m5 5H9'
  };
  let hasActiveSession = false;
  let sessionEndsAt = null;

  // «1 ч 27 мин» вместо «87»: так оставшееся время читается с одного взгляда.
  const formatMinutes = (minutes) => {
    const value = Math.max(0, Math.round(Number(minutes) || 0));
    const hours = Math.floor(value / 60);
    const rest = value % 60;
    return hours
      ? t(':hours ч :minutes мин', { hours, minutes: String(rest).padStart(2, '0') })
      : t(':count мин', { count: rest });
  };

  // Оставшееся время по данным сервера: обновляется на экране, пока сессия идёт.
  const showSessionTime = () => {
    if (!sessionEndsAt) return;
    const remainingMinutes = Math.max(0, Math.ceil((sessionEndsAt - serverNow()) / 60000));
    document.getElementById('remainingBig').textContent = formatMinutes(remainingMinutes);
    document.getElementById('profileRemaining').textContent = formatMinutes(remainingMinutes);
    document.getElementById('sessionExpires').textContent = formatDateTime(sessionEndsAt);
    document.getElementById('sessionExpiresRow').hidden = false;
  };
  setInterval(showSessionTime, 15000);

  // После конца сессии клуб может перезагрузить или выключить ПК: клиент видит отсчёт, пока не выберет тариф.
  const afterSessionMessages = {
    restart: ['Если не выбрать тариф, ПК перезагрузится через :time.', 'ПК перезагружается…'],
    shutdown: ['Если не выбрать тариф, ПК выключится через :time.', 'ПК выключается…']
  };
  let afterSessionTimer = null;
  const stopAfterSessionCountdown = () => {
    if (afterSessionTimer === null) return;
    clearInterval(afterSessionTimer);
    afterSessionTimer = null;
    setStatus(actionStatus, null);
  };
  const startAfterSessionCountdown = (action, seconds) => {
    const messages = afterSessionMessages[action];
    if (!messages) return;
    clearInterval(afterSessionTimer);
    const deadline = Date.now() + Math.max(0, Number(seconds) || 0) * 1000;
    if (afterSessionTimer === null) {
      const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
      toast(t(messages[0], { time: `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}` }), 'warning');
    }
    const tick = () => {
      const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
      const time = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`;
      setStatus(actionStatus, left > 0 ? messages[0] : messages[1], { time });
    };
    tick();
    afterSessionTimer = setInterval(tick, 1000);
  };

  const sessionEndMessages = {
    expired: 'Время сессии закончилось. Выберите тариф, чтобы продолжить.',
    server: 'Сессия завершена в клубе.',
    ended: 'Сессия завершена.',
    none: null
  };

  const showProfileStatus = () => {
    document.getElementById('profileStatus').textContent = hasActiveSession ? t('Активная сессия') : t('Нет активной сессии');
    // Без сессии в профиле — только «Нет активной сессии», без пустых строк тарифа и времени.
    document.getElementById('profileNoSession').hidden = hasActiveSession;
    document.getElementById('profileDetails').hidden = !hasActiveSession;
  };

  // Друзья, переводы и пересадка: только то, что клуб включил в настройках «Гости» (приходит с heartbeat).
  let guestFeatures = { friends: false, balance_transfer: false, self_transfer: false };
  // Вошёл, но без оплаченной сессии (не купил тариф или время кончилось): через столько минут
  // агент сам выходит из аккаунта и освобождает ПК (настройки клуба; 0 или null — не выходит).
  let idleLogoutMinutes = null;
  let idleSince = null;
  let idleWarned = false;
  let friendsData = { friends: [], incoming: [], outgoing: [] };
  const friendsBadge = document.getElementById('friendsBadge');
  const friendAddForm = document.getElementById('friendAddForm');
  const friendPhone = document.getElementById('friendPhone');
  const friendsStatus = document.getElementById('friendsStatus');
  const friendGroups = document.getElementById('friendGroups');
  const transferDialog = document.getElementById('transferDialog');
  const transferAmount = document.getElementById('transferAmount');
  const transferStatus = document.getElementById('transferStatus');
  const transferSubmit = document.getElementById('transferSubmit');
  const transferPassword = document.getElementById('transferPassword');
  // Глаз у пароля: показать введённое; очищенный пароль снова скрыт.
  const transferPasswordEye = document.getElementById('transferPasswordEye');
  const showTransferPassword = (shown) => {
    transferPassword.type = shown ? 'text' : 'password';
    transferPasswordEye.setAttribute('aria-pressed', String(shown));
    const label = shown ? t('Скрыть пароль') : t('Показать пароль');
    transferPasswordEye.title = label;
    transferPasswordEye.setAttribute('aria-label', label);
  };
  transferPasswordEye.addEventListener('click', () => {
    showTransferPassword(transferPassword.type === 'password');
    transferPassword.focus();
  });
  const clearTransferPassword = () => {
    transferPassword.value = '';
    showTransferPassword(false);
  };
  const seatButton = document.getElementById('btnSeat');
  const seatDialog = document.getElementById('seatDialog');
  const seatStatus = document.getElementById('seatStatus');
  let transferFriend = null;
  let chosenSeat = null;
  let seatError = null; // ошибка пересадки остаётся видна после обновления карты

  const openDialog = (dialog) => {
    if (typeof dialog.showModal === 'function') dialog.showModal();
    else dialog.setAttribute('open', '');
  };
  const closeDialog = (dialog) => {
    if (typeof dialog.close === 'function') dialog.close();
    else dialog.removeAttribute('open');
  };
  [[transferDialog, 'transferClose'], [seatDialog, 'seatClose']].forEach(([dialog, closeId]) => {
    document.getElementById(closeId).addEventListener('click', () => closeDialog(dialog));
    dialog.addEventListener('click', (event) => {
      if (event.target === dialog) closeDialog(dialog);
    });
  });

  const updateSeatButton = () => {
    seatButton.hidden = !(guestFeatures.self_transfer && hasActiveSession);
  };

  const friendRow = (friend, ...buttons) => {
    const row = document.createElement('div');
    row.className = 'friend-row';
    const who = document.createElement('div');
    who.className = 'friend-who';
    const name = document.createElement('strong');
    name.textContent = friend.name ?? '—';
    const phone = document.createElement('small');
    phone.textContent = friend.phone ?? '';
    who.append(name, phone);
    const actions = document.createElement('div');
    actions.className = 'friend-actions';
    actions.append(...buttons);
    row.append(who, actions);
    return row;
  };
  const friendButton = (label, onClick, className = 'friend-button') => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = className;
    button.textContent = t(label);
    button.addEventListener('click', onClick);
    return button;
  };
  const friendGroup = (title, rows) => {
    if (rows.length === 0) return null;
    const group = document.createElement('div');
    group.className = 'friend-group';
    const heading = document.createElement('h3');
    heading.textContent = t(title);
    group.append(heading, ...rows);
    return group;
  };
  // Списки друзей из ответа (ListFriendsResponse): id — friendship_id, телефон — маскированный.
  const friendList = (items) => (Array.isArray(items) ? items : []).map((friend) => ({
    id: Z.int64(friend.friendshipId), name: friend.name || null, phone: friend.maskedPhone || ''
  }));
  const applyFriends = (data, successKey = null) => {
    friendsData = { friends: friendList(data?.friends), incoming: friendList(data?.incoming), outgoing: friendList(data?.outgoing) };
    setStatus(friendsStatus, null);
    if (successKey) {
      toastKey(successKey, 'success');
      friendPhone.value = '';
    }
    renderFriends();
  };
  const friendFailed = (error) => {
    setStatus(friendsStatus, null);
    toast(error.text(t));
  };
  const sendFriendAction = (type, id) => {
    setStatus(friendsStatus, 'Отправляем…');
    const method = type === 'acceptFriend' ? 'SocialService/AcceptFriend' : 'SocialService/RemoveFriend';
    bridge.call(method, { friendshipId: String(id) }).then((result) => applyFriends(result.friends)).catch(friendFailed);
  };

  const renderFriends = () => {
    const friendsOn = guestFeatures.friends || guestFeatures.balance_transfer;
    friendsNavigation.hidden = !friendsOn;
    if (!friendsOn && currentView === 'friends') setView('session');
    friendAddForm.hidden = !guestFeatures.friends;
    // Новые заявки видны на пункте меню, даже когда страница друзей закрыта.
    const requests = guestFeatures.friends ? friendsData.incoming.length : 0;
    friendsBadge.hidden = requests === 0;
    friendsBadge.textContent = requests > 9 ? '9+' : String(requests);
    friendsNavigation.setAttribute('aria-label', requests ? t('Друзья: новых заявок — :count', { count: requests }) : t('Друзья'));
    const incoming = guestFeatures.friends
      ? friendsData.incoming.map((friend) => friendRow(friend,
        friendButton('Принять', () => sendFriendAction('acceptFriend', friend.id), 'friend-button accept'),
        friendButton('Отклонить', () => sendFriendAction('removeFriend', friend.id))))
      : [];
    const friends = friendsData.friends.map((friend) => friendRow(friend,
      ...(guestFeatures.balance_transfer ? [friendButton('Перевести', () => {
        transferFriend = friend;
        document.getElementById('transferFriend').textContent = `${friend.name ?? ''} · ${friend.phone ?? ''}`;
        transferAmount.value = '';
        clearTransferPassword();
        setStatus(transferStatus, null);
        transferSubmit.disabled = false;
        openDialog(transferDialog);
        transferAmount.focus();
      }, 'friend-button accept')] : []),
      friendButton('Удалить', () => sendFriendAction('removeFriend', friend.id))));
    const outgoing = friendsData.outgoing.map((friend) => friendRow(friend,
      friendButton('Отменить заявку', () => sendFriendAction('removeFriend', friend.id))));
    const groups = [
      friendGroup('Заявки в друзья', incoming),
      friendGroup('Мои друзья', friends),
      friendGroup('Отправленные заявки', outgoing)
    ].filter(Boolean);
    if (groups.length === 0) {
      const empty = document.createElement('p');
      empty.className = 'friend-empty';
      empty.textContent = t('Пока нет друзей.');
      groups.push(empty);
    }
    friendGroups.replaceChildren(...groups);
  };

  const requestFriends = () => {
    if (!webview || !(guestFeatures.friends || guestFeatures.balance_transfer)) return;
    bridge.call('SocialService/ListFriends').then((result) => applyFriends(result)).catch(friendFailed);
  };

  friendAddForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const phone = friendPhone.value.replace(/\D+/g, '');
    if (phone.length < 10) {
      toastKey('Введите 10 цифр номера после +7.', 'warning');
      return;
    }
    setStatus(friendsStatus, 'Отправляем…');
    bridge.call('SocialService/AddFriend', { phone: Z.normalizePhone(phone) ?? phone }).then((result) => {
      applyFriends(result.friends, result.accepted ? 'Вы теперь друзья.' : 'Заявка в друзья отправлена.');
    }).catch(friendFailed);
  });

  document.getElementById('transferForm').addEventListener('submit', (event) => {
    event.preventDefault();
    // Сумма — целые тиыны из строки (без float): «250.50» → 25050.
    const amount = Z.parseAmount(transferAmount.value);
    if (!transferFriend || amount === null) {
      toastKey('Введите сумму, например 500 или 250.50.', 'warning');
      return;
    }
    if (!transferPassword.value) {
      toastKey('Введите пароль от аккаунта, чтобы подтвердить перевод.', 'warning');
      transferPassword.focus();
      return;
    }
    transferSubmit.disabled = true;
    setStatus(transferStatus, 'Переводим…');
    // Пароль подтверждает перевод: только в запросе к службе, страница его не хранит.
    const request = { friendshipId: String(transferFriend.id), amountTiyn: String(amount), password: transferPassword.value };
    clearTransferPassword();
    bridge.call('SocialService/TransferBalance', request).then((result) => {
      transferSubmit.disabled = false;
      applyBalance(result.balance);
      closeDialog(transferDialog);
      toastKey('Перевод выполнен.', 'success');
    }).catch((error) => {
      transferSubmit.disabled = false;
      setStatus(transferStatus, null);
      toast(error.text(t));
    });
  });

  // Карта для пересадки — те же зоны и места ПК, что на карте клуба в панели (координаты оттуда же).
  const seatMap = document.getElementById('seatMap');
  const seatMapFrame = document.getElementById('seatMapFrame');
  const seatMapScroll = document.getElementById('seatMapScroll');
  const seatConfirm = document.getElementById('seatConfirm');
  const ZONE_COMPUTERS_LEFT = 12;
  const ZONE_COMPUTERS_TOP = 106;
  let seatPlan = { width: 0, height: 0 };

  const fitSeatMap = () => {
    if (!seatPlan.width) return;
    const scale = Math.min(1, (seatMapScroll.clientWidth - 2) / seatPlan.width);
    seatMap.style.transform = `scale(${scale})`;
    seatMapFrame.style.width = `${Math.ceil(seatPlan.width * scale)}px`;
    seatMapFrame.style.height = `${Math.ceil(seatPlan.height * scale)}px`;
  };
  window.addEventListener('resize', fitSeatMap);

  const showChosenSeat = (name = null) => {
    seatConfirm.disabled = chosenSeat === null;
    seatConfirm.textContent = chosenSeat === null ? t('Выберите свободный ПК') : t('Пересесть на ПК :name', { name });
  };

  const renderSeats = (map) => {
    chosenSeat = null;
    showChosenSeat();
    const zones = Array.isArray(map?.zones) ? map.zones : [];
    const computers = Array.isArray(map?.computers) ? map.computers : [];
    const number = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
    // Пустое поле слева и сверху не показываем: карта занимает всё окно.
    const left = zones.length ? Math.max(0, Math.min(...zones.map((zone) => number(zone.x))) - 20) : 0;
    const top = zones.length ? Math.max(0, Math.min(...zones.map((zone) => number(zone.y))) - 20) : 0;
    seatPlan = {
      width: Math.max(320, ...zones.map((zone) => number(zone.x) - left + number(zone.width) + 20)),
      height: Math.max(200, ...zones.map((zone) => number(zone.y) - top + number(zone.height) + 20))
    };
    seatMap.style.width = `${seatPlan.width}px`;
    seatMap.style.height = `${seatPlan.height}px`;

    seatMap.replaceChildren(...zones.map((zone) => {
      const box = document.createElement('section');
      box.className = `seat-zone ${typeof zone.theme === 'string' ? zone.theme : ''}`;
      Object.assign(box.style, { left: `${number(zone.x) - left}px`, top: `${number(zone.y) - top}px`, width: `${number(zone.width)}px`, height: `${number(zone.height)}px` });
      const title = document.createElement('h3');
      title.textContent = zone.name ?? '';
      const places = document.createElement('div');
      places.className = 'seat-zone-computers';
      places.style.left = `${ZONE_COMPUTERS_LEFT}px`;
      places.style.top = `${ZONE_COMPUTERS_TOP}px`;
      computers.filter((computer) => computer.zone_id === zone.id).forEach((computer) => {
        const current = computer.id === map.current_computer_id;
        const pc = document.createElement('button');
        pc.type = 'button';
        pc.className = `seat-pc ${current ? 'current' : computer.available ? 'free' : 'busy'}`;
        pc.disabled = !computer.available;
        pc.textContent = String(computer.number ?? '');
        pc.title = current ? `${computer.name ?? ''} · ${t('Вы здесь')}` : computer.name ?? '';
        pc.setAttribute('aria-label', pc.title);
        Object.assign(pc.style, { left: `${number(computer.x)}px`, top: `${number(computer.y)}px` });
        if (computer.available) {
          pc.addEventListener('click', () => {
            chosenSeat = computer.id;
            seatMap.querySelectorAll('.seat-pc').forEach((item) => item.classList.toggle('is-chosen', item === pc));
            showChosenSeat(computer.name ?? computer.number);
          });
        }
        places.append(pc);
      });
      box.append(title, places);
      return box;
    }));

    setStatus(seatStatus, computers.some((computer) => computer.available) ? 'Выберите свободный ПК на карте.' : 'Свободных ПК, где действует ваш тариф, сейчас нет.');
    fitSeatMap();
  };

  seatConfirm.addEventListener('click', () => {
    if (chosenSeat === null || !webview) return;
    seatConfirm.disabled = true;
    seatMap.querySelectorAll('.seat-pc').forEach((item) => { item.disabled = true; });
    setStatus(seatStatus, 'Переносим сессию…');
    bridge.call('GuestService/TransferSeat', { targetComputerId: String(chosenSeat) }).then((result) => {
      closeDialog(seatDialog);
      toast(result.message || '', 'success');
    }).catch((error) => {
      // ПК мог только что занять кто-то другой: карта загружается заново.
      seatError = error.text(t);
      setServerStatus(seatStatus, seatError);
      toast(seatError);
      requestSeats();
    });
  });

  // Карта клуба из GetSeatMapResponse → поля, которые рисует renderSeats (как у старого API).
  const seatMapData = (map) => ({
    zones: (Array.isArray(map?.zones) ? map.zones : []).map((zone) => ({
      id: Z.int64(zone.zoneId), name: zone.name || '', theme: zone.theme || '', x: zone.x, y: zone.y, width: zone.width, height: zone.height
    })),
    computers: (Array.isArray(map?.seats) ? map.seats : []).map((seat) => ({
      id: Z.int64(seat.computer?.computerId), zone_id: Z.int64(seat.zoneId), number: seat.computer?.number ?? '',
      name: seat.computer?.displayName || seat.computer?.number || '', x: seat.x, y: seat.y, available: seat.available === true
    })),
    current_computer_id: Z.int64(map?.currentComputerId)
  });
  const requestSeats = () => {
    bridge.call('GuestService/GetSeatMap').then((result) => {
      renderSeats(seatMapData(result));
      if (seatError) setServerStatus(seatStatus, seatError);
      seatError = null;
    }).catch((error) => toast(error.text(t)));
  };

  seatButton.addEventListener('click', () => {
    if (!webview) return;
    seatMap.replaceChildren();
    chosenSeat = null;
    showChosenSeat();
    setStatus(seatStatus, 'Ищем свободные ПК…');
    openDialog(seatDialog);
    requestSeats();
  });

  // Логи клиента: покупки, пополнения, переводы, пересадки и входы (сервер, страницами по 30).
  const historyList = document.getElementById('historyList');
  const historyEmpty = document.getElementById('historyEmpty');
  const historyStatus = document.getElementById('historyStatus');
  const historyMore = document.getElementById('historyMore');
  let historyEntries = [];
  let historyBefore = null;
  let historyLoading = false;
  let historyFilter = '';

  const historyAmount = (value, bonus) => {
    if (value === null || value === undefined) return null;
    const amount = Z.int64(value);
    const text = `${amount > 0 ? '+' : '−'}${Z.formatAmount(Math.abs(amount), intlLocale())}`;
    const element = document.createElement('span');
    element.className = `history-amount ${amount > 0 ? 'plus' : 'minus'}${bonus ? ' bonus' : ''}`;
    element.textContent = bonus ? t(':amount бонусов', { amount: text }) : text;
    return element;
  };

  const renderHistory = () => {
    historyList.replaceChildren(...historyEntries.map((entry) => {
      const item = document.createElement('li');
      item.className = `history-item ${typeof entry.kind === 'string' ? entry.kind : ''}`;
      const main = document.createElement('div');
      main.className = 'history-item-main';
      const title = document.createElement('strong');
      title.textContent = entry.title ?? '';
      const meta = document.createElement('small');
      meta.textContent = [entry.at ? formatDateTime(entry.at) : null, entry.note].filter(Boolean).join(' · ');
      main.append(title, meta);
      const amounts = document.createElement('div');
      amounts.className = 'history-item-amounts';
      amounts.append(...[historyAmount(entry.amount, false), historyAmount(entry.bonus_amount, true)].filter(Boolean));
      item.append(main, amounts);
      return item;
    }));
    historyEmpty.hidden = historyEntries.length > 0 || historyLoading;
    historyMore.hidden = historyBefore === null;
    historyMore.disabled = historyLoading;
  };

  const requestHistory = (more = false) => {
    if (!webview || historyLoading) return;
    if (!more) {
      historyEntries = [];
      historyBefore = null;
    }
    historyLoading = true;
    setStatus(historyStatus, 'Загружаем операции…');
    renderHistory();
    bridge.call('GuestService/ListHistory', { filter: historyFilters[historyFilter] ?? 'HISTORY_FILTER_UNSPECIFIED', pageToken: more ? historyBefore : '' })
      .then((result) => {
        // Записи журнала (HistoryEntry) → поля, которые рисует renderHistory; 0 — без изменения баланса.
        historyEntries = historyEntries.concat((Array.isArray(result.entries) ? result.entries : []).map((entry) => ({
          kind: entry.event || '',
          title: entry.title || '',
          at: entry.occurredAt || null,
          note: null,
          amount: Z.int64(entry.mainDeltaTiyn) || null,
          bonus_amount: Z.int64(entry.bonusDeltaTiyn) || null
        })));
        historyBefore = result.nextPageToken || null;
        setStatus(historyStatus, null);
      })
      .catch((error) => {
        setStatus(historyStatus, null);
        toast(error.text(t, 'Не удалось загрузить операции.'));
      })
      .finally(() => {
        historyLoading = false;
        renderHistory();
      });
  };
  const historyFilters = {
    '': 'HISTORY_FILTER_UNSPECIFIED',
    purchases: 'HISTORY_FILTER_PURCHASES',
    top_ups: 'HISTORY_FILTER_TOP_UPS',
    transfers: 'HISTORY_FILTER_TRANSFERS',
    visits: 'HISTORY_FILTER_VISITS'
  };
  document.getElementById('historyFilters').addEventListener('click', (event) => {
    const button = event.target.closest('[data-history-filter]');
    if (!button || historyLoading) return;
    historyFilter = button.dataset.historyFilter;
    document.querySelectorAll('[data-history-filter]').forEach((item) => {
      const active = item === button;
      item.classList.toggle('is-active', active);
      item.setAttribute('aria-pressed', String(active));
    });
    requestHistory();
  });
  historyMore.addEventListener('click', () => requestHistory(true));

  // Поминутно: цена часа зоны этого ПК / 60 × минуты (округление как на сервере, до тиына).
  const minuteCard = document.getElementById('minuteCard');
  const minuteInput = document.getElementById('minuteInput');
  const minuteBuy = document.getElementById('minuteBuy');
  let minuteOffer = null;

  const minuteCount = () => {
    const value = Number(minuteInput.value);
    const max = Number(minuteOffer?.maxMinutes) || 720;
    return Number.isInteger(value) && value >= 1 && value <= max ? value : null;
  };
  const renderMinutes = () => {
    minuteCard.hidden = !minuteOffer?.available;
    if (!minuteOffer?.available) return;
    const hourCents = Z.int64(minuteOffer.pricePerHourTiyn);
    const currency = minuteOffer.currency || 'KZT';
    minuteInput.max = String(Number(minuteOffer.maxMinutes) || 720);
    document.getElementById('minuteRate').textContent = t('Зона :zone · :price за час', { zone: minuteOffer.zoneName ?? '', price: formatPrice(hourCents, currency) });
    const minutes = minuteCount();
    document.getElementById('minutePrice').textContent = minutes === null
      ? t('Введите от 1 до :max минут', { max: minuteInput.max })
      : t(':minutes мин — :price', { minutes, price: formatPrice(Z.minutesPrice(hourCents, minutes), currency) });
    minuteBuy.disabled = minutes === null;
    renderWholeBalance(hourCents, currency);
  };
  // «На весь баланс»: минуты и цену считает сервер по основному балансу (GetMinuteOffer.wholeBalanceMinutes,
  // pricing.WholeBalanceMinutes — решение Q-008) и сам покупает их (GuestService/PlayWholeBalance).
  const minuteWhole = document.getElementById('minuteWhole');
  const minuteWholeBuy = document.getElementById('minuteWholeBuy');
  let wholeMinutes = 0;
  let wholePrice = '';
  const renderWholeBalance = (hourCents, currency) => {
    wholeMinutes = minuteOffer?.wholeBalance === true ? Number(minuteOffer.wholeBalanceMinutes) || 0 : 0;
    minuteWhole.hidden = wholeMinutes < 1;
    if (wholeMinutes >= 1) {
      wholePrice = formatPrice(Z.int64(minuteOffer.wholeBalancePriceTiyn), currency);
      minuteWholeBuy.textContent = t('На весь баланс: :time — :price', { time: formatMinutes(wholeMinutes), price: wholePrice });
    }
  };
  // Баланс изменился — минуты «на весь баланс» пересчитывает сервер.
  let offerBalance = null;
  onBalances = () => {
    if (!minuteOffer?.available) return;
    if (minuteOffer.wholeBalance === true && balances[0] !== offerBalance) {
      offerBalance = balances[0];
      requestMinutes();
      return;
    }
    renderMinutes();
  };
  minuteWholeBuy.addEventListener('click', async () => {
    if (wholeMinutes < 1 || !webview) return;
    const minutes = wholeMinutes;
    const ok = await askConfirm(t('На весь баланс'), t('Купить :time за :price с основного баланса? Ушли раньше — неиспользованные минуты вернутся на баланс.', { time: formatMinutes(minutes), price: wholePrice }), t('Купить'));
    if (!ok) return;
    minuteWholeBuy.disabled = true;
    minuteBuy.disabled = true;
    tariffCatalog.querySelectorAll('.tariff-purchase').forEach((item) => { item.disabled = true; });
    setStatus(tariffPageStatus, 'Оформляем тариф…');
    purchaseTime('GuestService/PlayWholeBalance', {});
  });
  const requestMinutes = () => {
    if (!webview || userName === null) return;
    bridge.call('GuestService/GetMinuteOffer').then((result) => {
      minuteOffer = result;
      offerBalance = balances[0];
    }).catch(() => {
      minuteOffer = null;
    }).finally(renderMinutes);
  };

  // Итог покупки времени (тариф, минуты, абонемент): баланс — из ответа, сессию присылает служба (снимок).
  const purchaseTime = async (method, params) => {
    let ok = false;
    let message = null;
    try {
      const result = await bridge.call(method, params);
      ok = true;
      message = result.purchase?.message || null;
      applyBalance(result.purchase?.balance);
    } catch (error) {
      message = error.message || null;
    }
    tariffCatalog.querySelectorAll('.tariff-purchase').forEach((item) => {
      item.disabled = false;
    });
    minuteWholeBuy.disabled = false;
    renderMinutes();
    setStatus(tariffPageStatus, null);
    toast(message ?? t(ok ? 'Тариф подключён.' : 'Не удалось оформить тариф.'), ok ? 'success' : 'error');
    if (ok) requestPackages();
  };

  // Поминутная оплата сама (настройка клуба): после входа — когда пройдёт пауза на выбор тарифа, после тарифа — сразу.
  // Решает и покупает сервер (решение Q-008, AG-12): состояние приходит в снимке службы (autoBilling), страница только
  // показывает «Поминутная оплата через m:ss» по часам сервера, как autoBillingTick старого агента.
  let autoBilling = null;
  const autoBillingWaits = () => userName !== null && !hasActiveSession && autoBilling?.waiting === true;
  let autoBillingShown = false;
  const autoBillingTick = () => {
    if (!autoBillingWaits()) {
      if (autoBillingShown && !hasActiveSession) document.getElementById('remainingBig').textContent = t('Нет активной сессии');
      autoBillingShown = false;
      return;
    }
    const endsAt = Z.timestampMs(autoBilling.pauseEndsAt);
    const left = endsAt === null ? 0 : endsAt - serverNow();
    const seconds = Math.max(0, Math.ceil(left / 1000));
    autoBillingShown = true;
    document.getElementById('remainingBig').textContent = t('Поминутная оплата через :time', { time: `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}` });
  };
  setInterval(autoBillingTick, 1000);

  minuteInput.addEventListener('input', renderMinutes);
  minuteCard.querySelectorAll('[data-minutes]').forEach((button) => button.addEventListener('click', () => {
    minuteInput.value = button.dataset.minutes;
    renderMinutes();
  }));
  minuteBuy.addEventListener('click', async () => {
    const minutes = minuteCount();
    if (minutes === null || !webview) return;
    const balance = await askBalance(t('Поминутно'), document.getElementById('minutePrice').textContent, Number(minuteOffer?.bonusMaxPercent) || 0, minuteOffer?.mixedPayment === true);
    if (!balance) return;
    minuteBuy.disabled = true;
    tariffCatalog.querySelectorAll('.tariff-purchase').forEach((item) => { item.disabled = true; });
    setStatus(tariffPageStatus, 'Оформляем тариф…');
    purchaseTime('GuestService/PurchaseMinutes', { minutes, balanceSource: balanceSource(balance) });
  });

  // Абонементы: сохранённое время тарифа с ограничением использования; запуск без оплаты.
  const packageSection = document.getElementById('packageSection');
  const packageList = document.getElementById('packageList');
  let packages = [];
  const requestPackages = () => {
    if (!webview || userName === null) return;
    bridge.call('GuestService/ListPackages').then((result) => {
      packages = (Array.isArray(result.packages) ? result.packages : []).map((item) => ({
        id: Z.int64(item.packageId), name: item.name || '', minutes: Number(item.minutesLeft) || 0, expires_at: item.expiresAt
      }));
    }).catch(() => {
      packages = [];
    }).finally(renderPackages);
  };
  const renderPackages = () => {
    packageSection.hidden = packages.length === 0;
    packageList.replaceChildren(...packages.map((item) => {
      const row = document.createElement('div');
      row.className = 'package-item';
      const info = document.createElement('div');
      const name = document.createElement('strong');
      name.textContent = item.name;
      const left = document.createElement('span');
      left.textContent = t('Осталось :time · до :date', { time: formatTariffDuration(item.minutes), date: formatDateTime(item.expires_at) });
      info.append(name, document.createElement('br'), left);
      const start = document.createElement('button');
      start.type = 'button';
      start.className = 'primary';
      start.textContent = t('Запустить');
      start.addEventListener('click', () => {
        start.disabled = true;
        setStatus(tariffPageStatus, 'Запускаем абонемент…');
        purchaseTime('GuestService/StartPackage', { packageId: String(item.id) });
      });
      row.append(info, start);
      return row;
    }));
  };

  // Новости на главной: Zafiro (первая карточка) и клуб (вторая). Открываются здесь же, в окне агента.
  const newsDialog = document.getElementById('newsDialog');
  const newsCards = Array.from(document.querySelectorAll('[data-news-column]'));
  let news = { platform: [], club: [] };
  let newsOpen = { column: 'platform', index: 0 };
  const newsKickers = { platform: 'Новости Zafiro', club: 'Новости клуба' };

  const renderNewsCards = () => {
    newsCards.forEach((card) => {
      const latest = news[card.dataset.newsColumn]?.[0];
      const image = card.querySelector('img');
      const text = card.querySelector('.news-card-text');
      card.disabled = !latest;
      text.hidden = !latest;
      text.querySelector('strong').textContent = latest?.title ?? '';
      // Без картинки у новости (или пока её нет в кэше службы) остаётся встроенная картинка карточки.
      const src = latest?.image_url || image.dataset.fallback;
      if (image.getAttribute('src') !== src) image.setAttribute('src', src);
      card.setAttribute('aria-label', latest ? `${t(newsKickers[card.dataset.newsColumn])}: ${latest.title}` : t(newsKickers[card.dataset.newsColumn]));
    });
  };

  const showNews = () => {
    const list = news[newsOpen.column] ?? [];
    const item = list[newsOpen.index];
    if (!item) {
      closeDialog(newsDialog);
      return;
    }
    document.getElementById('newsKicker').textContent = t(newsKickers[newsOpen.column]);
    const image = document.getElementById('newsImage');
    image.hidden = !item.image_url;
    if (item.image_url) image.src = item.image_url;
    else image.removeAttribute('src');
    document.getElementById('newsTitle').textContent = item.title ?? '';
    document.getElementById('newsDate').textContent = item.published_at ? formatDateTime(item.published_at) : '';
    document.getElementById('newsBody').textContent = item.body ?? '';
    document.getElementById('newsPosition').textContent = list.length > 1 ? `${newsOpen.index + 1} / ${list.length}` : '';
    document.getElementById('newsNewer').disabled = newsOpen.index === 0;
    document.getElementById('newsOlder').disabled = newsOpen.index >= list.length - 1;
  };

  // Картинки новости нет в кэше службы и сервер недоступен — в окне новости без картинки.
  document.getElementById('newsImage').addEventListener('error', (event) => { event.target.hidden = true; });
  // Картинка новости не загрузилась (нет связи) — карточка показывает свою встроенную.
  newsCards.forEach((card) => {
    const image = card.querySelector('img');
    image.addEventListener('error', () => {
      if (image.getAttribute('src') !== image.dataset.fallback) image.setAttribute('src', image.dataset.fallback);
    });
  });
  newsCards.forEach((card) => card.addEventListener('click', () => {
    if (!news[card.dataset.newsColumn]?.length) return;
    newsOpen = { column: card.dataset.newsColumn, index: 0 };
    showNews();
    openDialog(newsDialog);
  }));
  document.getElementById('newsNewer').addEventListener('click', () => {
    newsOpen.index = Math.max(0, newsOpen.index - 1);
    showNews();
  });
  document.getElementById('newsOlder').addEventListener('click', () => {
    newsOpen.index += 1;
    showNews();
  });
  document.getElementById('newsClose').addEventListener('click', () => closeDialog(newsDialog));
  newsDialog.addEventListener('click', (event) => {
    if (event.target === newsDialog) closeDialog(newsDialog);
  });
  // Новости — при входе гостя и при переходе на главную (без опроса по таймеру).
  const newsItem = (item) => ({
    title: item.title || '', body: item.body || '', published_at: item.publishedAt || null,
    // Картинка новости — через кэш службы (img.zafiro.invalid/news/<id>, AG-13); id — только целое.
    image_url: item.imageUrl && /^[1-9][0-9]{0,18}$/.test(String(item.newsId ?? '')) ? `${imageHost}news/${item.newsId}` : ''
  });
  const requestNews = () => {
    if (!webview || userName === null) return;
    bridge.call('GuestService/ListNews').then((result) => {
      news = {
        platform: (Array.isArray(result.platform) ? result.platform : []).map(newsItem),
        club: (Array.isArray(result.club) ? result.club : []).map(newsItem)
      };
      renderNewsCards();
      if (newsDialog.open) showNews();
    }).catch(() => {});
  };

  // Чат с клубом: сообщения клиента приходят кассиру и оператору в панель. Пока страница чата
  // открыта — проверка каждые 5 с, иначе каждые 30 с (значок новых ответов на пункте меню).
  const chatThread = document.getElementById('chatThread');
  const chatStatus = document.getElementById('chatStatus');
  const chatInput = document.getElementById('chatInput');
  const chatBadge = document.getElementById('chatBadge');
  let chatAfter = 0;
  let chatUnread = 0;
  let chatLoading = false;

  const showChatBadge = () => {
    chatBadge.hidden = chatUnread === 0;
    chatBadge.textContent = chatUnread > 9 ? '9+' : String(chatUnread);
  };
  // ChatMessage контракта → поля, которые рисует appendChat.
  const chatItem = (message) => message ? ({
    id: Z.int64(message.messageId),
    from_staff: message.fromStaff === true,
    author: message.authorName || null,
    body: message.body || '',
    at: message.sentAt ? formatDateTime(message.sentAt) : null
  }) : null;
  const appendChat = (message) => {
    if (!message || message.id <= chatAfter) return;
    chatAfter = message.id;
    const item = document.createElement('li');
    item.className = `chat-message ${message.from_staff ? 'staff' : 'mine'}`;
    const text = document.createElement('p');
    text.textContent = message.body ?? '';
    const meta = document.createElement('small');
    meta.textContent = [message.at, message.from_staff ? (message.author ?? t('Клуб')) : null].filter(Boolean).join(' · ');
    item.append(text, meta);
    chatThread.append(item);
    chatThread.scrollTop = chatThread.scrollHeight;
    if (message.from_staff && currentView !== 'chat') {
      chatUnread += 1;
      showChatBadge();
    }
  };
  // Новые сообщения приходят push (событие chatMessage, счётчики guestCounters); загрузка — при входе и по счётчику.
  const requestChat = () => {
    if (!webview || chatLoading || userName === null) return;
    chatLoading = true;
    const first = chatAfter === 0;
    const before = chatUnread;
    bridge.call('SocialService/ListChatMessages', { afterMessageId: String(chatAfter) }).then((result) => {
      (Array.isArray(result.messages) ? result.messages : []).map(chatItem).forEach(appendChat);
      // Первая загрузка — история без значка: новыми считаются ответы после неё.
      if (first) {
        chatUnread = before;
        showChatBadge();
      }
    }).catch(() => {}).finally(() => {
      chatLoading = false;
    });
  };

  document.getElementById('chatForm').addEventListener('submit', (event) => {
    event.preventDefault();
    const body = chatInput.value.trim();
    if (!body || !webview) return;
    setStatus(chatStatus, 'Отправляем…');
    bridge.call('SocialService/SendChatMessage', { body }).then((result) => {
      setStatus(chatStatus, null);
      chatInput.value = '';
      appendChat(chatItem(result.message));
    }).catch((error) => {
      setStatus(chatStatus, null);
      toast(error.text(t, 'Не удалось отправить сообщение.'));
    });
  });
  chatInput.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      document.getElementById('chatForm').requestSubmit();
    }
  });

  const setSessionActive = (active) => {
    hasActiveSession = active;
    endButton.disabled = false;
    endButtonLabel.textContent = active ? t('Завершить сессию') : t('Выйти');
    endButtonIcon.setAttribute('d', active ? endIconPaths.end : endIconPaths.logout);
    document.getElementById('extendLabel').textContent = active ? t('Продлить сессию') : t('Выбрать тариф');
    showProfileStatus();
    updateSeatButton();
  };

  const showStartedAt = () => {
    document.getElementById('profileStartedAt').textContent = sessionStartedAt ? formatDateTime(sessionStartedAt) : '—';
  };

  const clearSessionDetails = () => {
    sessionEndsAt = null;
    sessionStartedAt = null;
    document.getElementById('remainingBig').textContent = t('Нет активной сессии');
    document.getElementById('sessionExpires').textContent = '';
    document.getElementById('sessionExpiresRow').hidden = true;
    document.getElementById('profileTariff').textContent = '—';
    document.getElementById('profileRemaining').textContent = '—';
    delete document.getElementById('profileRemaining').dataset.minutes;
    showStartedAt();
    showProfileStatus();
  };

  endButton.addEventListener('click', async () => {
    if (!webview) {
      toastKey('Действие доступно только в приложении.', 'warning');
      return;
    }

    const ending = hasActiveSession;
    const confirmed = ending
      ? await askConfirm(t('Завершить сессию?'), t('Минуты, купленные поминутно, вернутся на баланс, а остаток тарифа с ограничением использования сохранится абонементом. Остальное время тарифа не возвращается.'), t('Завершить сессию'))
      : await askConfirm(t('Выйти из аккаунта?'), t('Чтобы снова играть на этом ПК, нужно будет войти заново.'), t('Выйти'));
    // Пока окно было открыто, сессия могла закончиться: действие — то, что клиент подтвердил.
    if (!confirmed || ending !== hasActiveSession) return;

    endButton.disabled = true;
    setStatus(actionStatus, hasActiveSession ? 'Завершаем сессию…' : 'Выходим…');
    // Завершение сессии не выводит из аккаунта, «Выйти» — выводит (agent.SignOut: служба отзывает токен гостя).
    if (!hasActiveSession) {
      signOut();
      return;
    }
    try {
      const result = await bridge.call('GuestService/EndSession', { sessionId: currentSessionId ?? '' });
      applyBalance(result.balance);
      endSessionLocally('ended');
      // Что вернулось на баланс и что сохранилось абонементом.
      const parts = [];
      if (Z.int64(result.refundedMainTiyn) > 0) parts.push(t('Возвращено на баланс: :amount', { amount: formatPrice(Z.int64(result.refundedMainTiyn)) }));
      if (Z.int64(result.refundedBonusTiyn) > 0) parts.push(t('Возвращено бонусов: :amount', { amount: formatPrice(Z.int64(result.refundedBonusTiyn)) }));
      if (Number(result.keptMinutes) > 0) parts.push(t('Сохранено в абонементах: :time', { time: formatTariffDuration(result.keptMinutes) }));
      if (parts.length) toast(parts.join(' · '), 'success');
      requestPackages();
    } catch (error) {
      endButton.disabled = false;
      setStatus(actionStatus, null);
      toast(error.text(t, 'Не удалось завершить сессию.'));
    }
  });

  const signOut = () => {
    bridge.call('agent.SignOut').catch((error) => {
      endButton.disabled = false;
      setStatus(actionStatus, null);
      toast(error.text(t));
    });
  };

  // Выход без оплаченной сессии через idle_logout_minutes (настройка клуба): выводит служба по «бездействию», которое
  // сообщает страница (agent.ReportActivity) — столько секунд гость в аккаунте без сессии, как считал старый агент
  // (idleLogoutTick); пока ждёт поминутная оплата (пауза на выбор тарифа) — 0. Предупреждение за минуту — здесь.
  let reportedIdle = -1;
  const idleLogoutTick = () => {
    const minutes = Number(idleLogoutMinutes);
    let idleSeconds = 0;
    if (!webview || userName === null || hasActiveSession || autoBillingWaits() || !Number.isFinite(minutes) || minutes <= 0) {
      idleSince = null;
      idleWarned = false;
    } else {
      idleSince ??= Date.now();
      idleSeconds = Math.max(0, Math.floor((Date.now() - idleSince) / 1000));
      const left = idleSince + minutes * 60000 - Date.now();
      if (!idleWarned && left > 0 && left <= 60000) {
        idleWarned = true;
        toastKey('Нет активной сессии: через минуту вы выйдете из аккаунта. Выберите тариф, чтобы остаться.', 'warning');
      }
    }
    if (webview && (idleSeconds !== reportedIdle)) {
      reportedIdle = idleSeconds;
      bridge.call('agent.ReportActivity', { idleSeconds }).catch(() => {
        reportedIdle = -1;
      });
    }
  };
  setInterval(idleLogoutTick, 5000);

  homeNavigation.addEventListener('click', () => {
    setView('session');
    requestNews();
  });
  shopNavigation.addEventListener('click', () => setView('shop'));
  historyNavigation.addEventListener('click', () => setView('history'));
  tariffNavigation.addEventListener('click', () => {
    setView('tariffs');
    requestMinutes();
    requestPackages();
  });
  profileNavigation.addEventListener('click', () => {
    setView('profile');
  });
  chatNavigation.addEventListener('click', () => {
    setView('chat');
    chatUnread = 0;
    showChatBadge();
    requestChat();
    chatInput.focus();
  });
  friendsNavigation.addEventListener('click', () => {
    setView('friends');
    requestFriends();
  });
  historyNavigation.addEventListener('click', () => requestHistory());
  pageToggle.addEventListener('click', () => setView(currentView === 'session' ? 'shop' : 'session'));

  function showProfileName() {
    const name = userName ?? t('Гость');
    document.getElementById('userName').textContent = name;
    document.getElementById('profileDisplayName').textContent = name;
    document.getElementById('profileAvatarText').textContent = name.trim().charAt(0).toLocaleUpperCase(intlLocale()) || '?';
    // Имя и пароль меняет только вошедший клиент; поле имени — с текущим именем, пока его не правят.
    profileAccount.hidden = userName === null;
    if (document.activeElement !== profileNameInput) profileNameInput.value = userName ?? '';
  }

  // Профиль: своё имя и пароль аккаунта (пароль — с текущим, другие устройства выйдут).
  const profileAccount = document.getElementById('profileAccount');
  const profileNameForm = document.getElementById('profileNameForm');
  const profileNameInput = document.getElementById('profileNameInput');
  const profileNameSubmit = document.getElementById('profileNameSubmit');
  const profilePasswordForm = document.getElementById('profilePasswordForm');
  const profilePasswordStatus = document.getElementById('profilePasswordStatus');
  const profilePasswordSubmit = document.getElementById('profilePasswordSubmit');
  const profilePasswords = ['profileCurrentPassword', 'profileNewPassword', 'profileRepeatPassword'].map((id) => document.getElementById(id));
  const showPasswordField = (button, shown) => {
    button.previousElementSibling.type = shown ? 'text' : 'password';
    button.setAttribute('aria-pressed', String(shown));
    const label = shown ? t('Скрыть пароль') : t('Показать пароль');
    button.title = label;
    button.setAttribute('aria-label', label);
  };
  document.querySelectorAll('[data-password-eye]').forEach((button) => button.addEventListener('click', () => {
    showPasswordField(button, button.previousElementSibling.type === 'password');
    button.previousElementSibling.focus();
  }));
  const clearProfilePasswords = () => {
    profilePasswords.forEach((input) => { input.value = ''; });
    document.querySelectorAll('[data-password-eye]').forEach((button) => showPasswordField(button, false));
    setStatus(profilePasswordStatus, null);
  };
  profileNameForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const name = profileNameInput.value.trim();
    if (name.length < 2) {
      toastKey('Имя — не короче 2 символов.', 'warning');
      return;
    }
    if (!webview) return;
    profileNameSubmit.disabled = true;
    bridge.call('GuestService/UpdateName', { name }).then((result) => {
      profileNameSubmit.disabled = false;
      if (typeof result.name === 'string' && result.name) userName = result.name;
      profileNameInput.blur();
      showProfileName();
      toastKey('Имя сохранено.', 'success');
    }).catch((error) => {
      profileNameSubmit.disabled = false;
      toast(error.text(t));
    });
  });
  profilePasswordForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const [current, next, repeat] = profilePasswords.map((input) => input.value);
    if (next.length < 6) {
      setStatus(profilePasswordStatus, 'Пароль — не короче 6 символов.');
      return;
    }
    if (next !== repeat) {
      setStatus(profilePasswordStatus, 'Пароли не совпадают.');
      return;
    }
    if (!webview) return;
    profilePasswordSubmit.disabled = true;
    setStatus(profilePasswordStatus, 'Сохраняем…');
    bridge.call('GuestService/ChangePassword', { currentPassword: current, newPassword: next }).then(() => {
      profilePasswordSubmit.disabled = false;
      clearProfilePasswords();
      toastKey('Пароль изменён.', 'success');
    }).catch((error) => {
      profilePasswordSubmit.disabled = false;
      setServerStatus(profilePasswordStatus, error.text(t));
    });
  });

  const showPcLabel = () => {
    const label = document.getElementById('pcLabel');
    if (!label.dataset.number) return;
    label.textContent = t('ПК :number', { number: label.dataset.number }) + (label.dataset.zone ? ' · ' + label.dataset.zone : '');
    label.hidden = false;
  };

  // Язык выбирает клиент; после его выхода агент возвращает язык клуба.
  const renderLanguageSwitch = () => {
    const box = document.getElementById('langSwitch');
    const languages = app.languages();
    box.replaceChildren(...languages.map(({ code, name }) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'lang-button';
      button.textContent = languageLabels[code] ?? code.toUpperCase();
      button.title = name;
      button.lang = code;
      button.setAttribute('aria-pressed', String(code === i18n.locale));
      button.addEventListener('click', () => {
        if (code !== i18n.locale) app.setLanguage(code);
      });
      return button;
    }));
    box.hidden = languages.length < 2;
  };

  const applyLanguage = () => {
    document.documentElement.lang = i18n.locale;
    document.querySelectorAll('[data-i18n]').forEach((element) => {
      element.textContent = t(element.dataset.i18n);
    });
    [['data-i18n-aria-label', 'aria-label'], ['data-i18n-title', 'title'], ['data-i18n-alt', 'alt']].forEach(([source, target]) => {
      document.querySelectorAll(`[${source}]`).forEach((element) => element.setAttribute(target, t(element.getAttribute(source))));
    });
    statusKeys.forEach(([key, params], element) => {
      element.textContent = key ? t(key, params) : '';
    });
    setView(currentView);
    setSessionActive(hasActiveSession);
    showProfileName();
    showStartedAt();
    applyBalances(...balances);
    renderCart();
    if (lastTariffs) renderTariffs(lastTariffs);
    if (sessionEndsAt) showSessionTime();
    else if (document.getElementById('profileRemaining').dataset.minutes) {
      document.getElementById('profileRemaining').textContent = formatMinutes(Number(document.getElementById('profileRemaining').dataset.minutes));
    }
    showPcLabel();
    renderLanguageSwitch();
    renderFriends();
    renderHistory();
    renderMinutes();
    renderPackages();
    renderNewsCards();
    if (newsDialog.open) showNews();
    if (!sessionEndsAt && !hasActiveSession) clearSessionDetails();
  };

  // ---------------------------------------------------------------------------------------------
  // Состояние из службы: снимок (гость, баланс, сессия, настройки клуба, ПК) и события (чат, счётчики).
  // ---------------------------------------------------------------------------------------------

  let currentSessionId = null;
  const afterSessionActions = { AFTER_SESSION_ACTION_RESTART: 'restart', AFTER_SESSION_ACTION_SHUTDOWN: 'shutdown' };
  let clubConfig = null;

  // Сессия закончилась: агент снова поверх рабочего стола и ждёт оплаты; клиент остаётся в своём аккаунте.
  // Перенесено из старого агента: MainWindow.xaml.cs EndSessionLocally(), main.js (sessionEnded, afterSession).
  const endSessionLocally = (reason) => {
    currentSessionId = null;
    setSessionActive(false);
    clearSessionDetails();
    const ended = sessionEndMessages[reason] === undefined ? sessionEndMessages.ended : sessionEndMessages[reason];
    setStatus(actionStatus, ended);
    if (ended) toastKey(ended, 'warning');
    const action = afterSessionActions[clubConfig?.afterSessionAction];
    if (action) startAfterSessionCountdown(action, Z.durationSeconds(clubConfig?.afterSessionDelay));
  };

  // Гость вышел: на экране не остаётся его баланса, корзины, чата и истории.
  const resetGuest = () => {
    friendsData = { friends: [], incoming: [], outgoing: [] };
    renderFriends();
    historyEntries = [];
    historyBefore = null;
    renderHistory();
    packages = [];
    renderPackages();
    minuteOffer = null;
    renderMinutes();
    closeDialog(newsDialog);
    finishPay(null);
    chatThread.replaceChildren();
    chatAfter = 0;
    chatUnread = 0;
    showChatBadge();
    closeDialog(transferDialog);
    closeDialog(seatDialog);
    closeDialog(cartDialog);
    currentSessionId = null;
    setSessionActive(false);
    clearSessionDetails();
    userName = null;
    showProfileName();
    applyBalances(null, null);
    stopAfterSessionCountdown();
    cart.clear();
    shopPaying = false;
    products = [];
    renderCart();
    setStatus(actionStatus, null);
    clearTransferPassword();
    clearProfilePasswords();
    if (confirmResolve) finishConfirm(false);
    historyFilter = '';
    document.querySelectorAll('[data-history-filter]').forEach((item) => {
      const active = item.dataset.historyFilter === '';
      item.classList.toggle('is-active', active);
      item.setAttribute('aria-pressed', String(active));
    });
    autoBilling = null;
    setView('session');
  };

  const requestTariffs = () => {
    if (!webview || userName === null) return;
    setStatus(tariffPageStatus, 'Загружаем тарифы…');
    bridge.call('GuestService/ListTariffs').then((result) => {
      renderTariffs(Array.isArray(result.tariffs) ? result.tariffs : []);
    }).catch((error) => {
      renderTariffs([]);
      setStatus(tariffPageStatus, 'Не удалось загрузить тарифы. Проверьте подключение.');
      toast(error.text(t, 'Не удалось загрузить тарифы. Проверьте подключение.'));
    });
  };

  // Гость вошёл (вход, регистрация, QR): данные лаунчера загружаются один раз, дальше — push.
  const loadGuest = () => {
    requestTariffs();
    requestMinutes();
    requestPackages();
    requestNews();
    requestChat();
    requestFriends();
    fetchProducts();
  };

  app.onSnapshot((snap) => {
    const serverTime = Z.timestampMs(snap.serverTime);
    if (serverTime !== null) clockOffset = serverTime - Date.now();
    clubConfig = snap.config ?? null;

    // Название клуба и номер ПК.
    const clubName = clubConfig?.clubName || snap.clubName;
    if (clubName) document.getElementById('clubName').textContent = clubName;
    if (snap.computer?.number) {
      const label = document.getElementById('pcLabel');
      label.dataset.number = snap.computer.number;
      label.dataset.zone = snap.computer.zoneName ?? '';
      showPcLabel();
    }

    // Друзья, переводы, пересадка, магазин: только то, что клуб включил в настройках «Гости».
    const features = clubConfig?.guestFeatures ?? {};
    guestFeatures = {
      friends: features.friends === true,
      balance_transfer: features.balanceTransfer === true,
      self_transfer: features.selfTransfer === true
    };
    shopEnabled = features.shop === true;
    idleLogoutMinutes = Number.isFinite(Number(clubConfig?.idleLogoutMinutes)) ? Number(clubConfig.idleLogoutMinutes) : null;
    setView(currentView);

    // Гость.
    if (!snap.guest) {
      if (userName !== null) resetGuest();
      return;
    }
    const firstSnapshot = userName === null;
    userName = snap.guest.name || t('Гость');
    showProfileName();
    applyBalance(snap.balance);
    if (firstSnapshot) loadGuest();

    // Сессия по данным сервера (служба держит её по стриму): покупка, продление, перезапуск агента.
    const session = snap.session?.sessionId ? snap.session : null;
    autoBilling = snap.autoBilling ?? null;
    // Профиль: тариф текущей сессии и версия агента (AG-17, как SessionPayload старого агента).
    document.getElementById('profileTariff').textContent = session?.tariffName || '—';
    const profileVersion = document.getElementById('profileVersion');
    profileVersion.hidden = !snap.agentVersion;
    if (snap.agentVersion) profileVersion.textContent = t('Версия агента: :version', { version: snap.agentVersion });
    if (session) {
      currentSessionId = session.sessionId;
      sessionEndsAt = Z.timestampMs(session.endsAt);
      sessionStartedAt = Z.timestampMs(session.startedAt);
      showStartedAt();
      showSessionTime();
      setSessionActive(true);
      stopAfterSessionCountdown();
    } else if (hasActiveSession) {
      // Сессия закончилась не по кнопке: время вышло или её закрыли в клубе.
      const timeIsUp = sessionEndsAt !== null && sessionEndsAt <= serverNow() + 30000;
      endSessionLocally(timeIsUp ? 'expired' : 'server');
    }
    updateSeatButton();
    renderFriends();
    if (minuteOffer === null && !firstSnapshot) requestMinutes();
  });

  app.onEvent((event) => {
    if (userName === null) return;
    if (event.chatMessage?.message) {
      appendChat(chatItem(event.chatMessage.message));
      return;
    }
    // Счётчики от сервера: друзья и чат запрашиваются, только когда в них появилось новое.
    if (event.guestCounters) {
      if ((Number(event.guestCounters.pendingFriendRequests) || 0) !== friendsData.incoming.length) requestFriends();
      if (Z.int64(event.guestCounters.lastStaffChatMessageId) > chatAfter) requestChat();
    }
  });

  app.onLanguage(applyLanguage);

  if (!webview) setStatus(tariffPageStatus, 'Запустите приложение, чтобы загрузить тарифы.');
  renderCart();
  showProfileName();
  applyLanguage();
})();
