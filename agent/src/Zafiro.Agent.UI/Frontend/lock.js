// Экран входа / блокировки, регистрация ПК и PIN сотрудника. Перенос WPF-окон старого агента в HTML 1:1 по виду:
// Views/ClubSetupWindow.xaml(.cs) — вход по телефону и паролю, регистрация, восстановление пароля по коду из Telegram,
// вход с телефона по QR-коду, кнопки выключения/перезагрузки (второе нажатие подтверждает), языки, номер ПК;
// Views/StaffPinWindow.xaml(.cs) — PIN сотрудника (Ctrl+Alt+P). Ключа установки больше нет: ПК подключается одноразовым
// кодом из панели клуба (agent.Enroll). Пароль гостя уходит в службу внутри запроса и нигде не хранится.
(() => {
  'use strict';
  const Z = window.Zafiro;
  const app = window.ZafiroApp;
  const { i18n, bridge } = app;
  const t = i18n.t;
  const $ = (id) => document.getElementById(id);

  const title = $('lockTitle');
  const club = $('lockClub');
  const setupForm = $('setupForm');
  const setupCode = $('setupCode');
  const setupError = $('setupError');
  const setupNotice = $('setupNotice');
  const setupSubmit = $('setupSubmit');
  const loginForm = $('loginForm');
  const phone = $('loginPhone');
  const password = $('loginPassword');
  const confirm = $('loginConfirm');
  const name = $('loginName');
  const code = $('loginCode');
  const error = $('loginError');
  const notice = $('loginNotice');
  const submit = $('loginSubmit');
  const registerToggle = $('loginRegisterToggle');
  const forgotToggle = $('loginForgotToggle');
  const qr = $('loginQr');
  const qrImage = $('loginQrImage');
  const inputLanguage = $('loginInputLanguage');

  // Режимы формы входа (ClubSetupWindow: _registering, _recovery).
  let registering = false;
  let recovery = 'none'; // none | phone | code
  let busy = false;
  let screen = '';
  let qrRunning = false;

  // Тексты по ключу показываются заново при смене языка.
  const keyed = new Map();
  const setKey = (element, key, params = {}) => {
    keyed.set(element, [key, params]);
    element.textContent = key ? t(key, params) : '';
  };
  const setText = (element, text) => {
    keyed.delete(element);
    element.textContent = text ?? '';
  };
  const showNotice = (element, key) => {
    element.hidden = !key;
    if (key) setKey(element, key);
  };

  // ---------------------------------------------------------------------------------------------
  // Номер ПК, картинка клуба, языки, кнопки питания.
  // ---------------------------------------------------------------------------------------------

  const renderPc = (snap) => {
    const number = snap?.computer?.number;
    $('lockPc').hidden = !number;
    if (number) {
      setKey($('lockPcNumber'), 'ПК :number', { number });
      setText($('lockPcZone'), snap.computer.zoneName ?? '');
    }
  };

  const renderLanguages = () => {
    const box = $('lockLanguages');
    const list = app.languages();
    box.hidden = list.length < 2;
    box.replaceChildren(...list.map(({ code: lang, name: label }) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `lock-language${lang === i18n.locale ? ' is-current' : ''}`;
      button.textContent = { ru: 'RU', kk: 'ҚАЗ', en: 'EN' }[lang] ?? lang.toUpperCase();
      button.title = label;
      button.setAttribute('aria-label', label);
      button.addEventListener('click', () => {
        if (busy) return;
        setText(error, '');
        app.setLanguage(lang);
      });
      return button;
    }));
  };

  // Первое нажатие — подсказка «нажмите ещё раз» на 5 секунд, второе — выключение или перезагрузка.
  let powerArmed = null;
  let powerTimer = null;
  const showPower = () => {
    $('lockShutdown').classList.toggle('is-armed', powerArmed === 'shutdown');
    $('lockRestart').classList.toggle('is-armed', powerArmed === 'restart');
    const hint = $('lockPowerHint');
    hint.hidden = powerArmed === null;
    if (powerArmed) setKey(hint, powerArmed === 'restart' ? 'Нажмите ещё раз, чтобы перезагрузить ПК.' : 'Нажмите ещё раз, чтобы выключить ПК.');
  };
  const powerClicked = (action) => {
    clearTimeout(powerTimer);
    if (powerArmed !== action) {
      powerArmed = action;
      powerTimer = setTimeout(() => {
        powerArmed = null;
        showPower();
      }, 5000);
      showPower();
      return;
    }
    powerArmed = null;
    showPower();
    bridge.call('ui.Power', { action }).then((result) => {
      if (!result.ok) setKey(screen === 'setup' ? setupError : error, 'Windows не дал выключить ПК. Обратитесь к администратору клуба.');
    }).catch(() => {});
  };
  $('lockShutdown').addEventListener('click', () => powerClicked('shutdown'));
  $('lockRestart').addEventListener('click', () => powerClicked('restart'));

  // Язык ввода Windows у поля пароля (панели задач в киоске нет): РУС, ҚАЗ, ENG; нажатие — следующий.
  const showInputLanguage = (next = false) => {
    if (!bridge.available) {
      inputLanguage.hidden = true;
      return;
    }
    bridge.call('ui.InputLanguage', { next }).then((result) => {
      inputLanguage.textContent = result.label ?? '';
      inputLanguage.title = t('Язык ввода: :language. Нажмите, чтобы сменить (или Alt+Shift).', { language: result.label ?? '' });
    }).catch(() => {});
  };
  inputLanguage.addEventListener('click', () => {
    showInputLanguage(true);
    password.focus();
  });
  password.addEventListener('focus', () => showInputLanguage(false));

  // Глаз у поля пароля; очищенный пароль снова скрыт — следующий гость не увидит свой ввод открытым.
  document.querySelectorAll('[data-lock-eye]').forEach((eye) => eye.addEventListener('click', () => {
    const input = eye.parentElement.querySelector('input');
    const shown = input.type === 'password';
    input.type = shown ? 'text' : 'password';
    eye.setAttribute('aria-pressed', String(shown));
    eye.setAttribute('aria-label', t(shown ? 'Скрыть пароль' : 'Показать пароль'));
    input.focus();
  }));
  const clearPasswords = () => {
    [password, confirm].forEach((input) => {
      input.value = '';
      input.type = 'password';
    });
    document.querySelectorAll('[data-lock-eye]').forEach((eye) => eye.setAttribute('aria-pressed', 'false'));
  };

  // Телефон по мере ввода — «700 123-45-67» (PhoneNumberFormatter.FormatLocal).
  phone.addEventListener('input', () => {
    const formatted = Z.formatLocalPhone(phone.value);
    if (formatted !== phone.value) phone.value = formatted;
  });

  // ---------------------------------------------------------------------------------------------
  // Тексты формы (ClubSetupWindow.ApplyTexts).
  // ---------------------------------------------------------------------------------------------

  const applyTexts = () => {
    keyed.forEach(([key, params], element) => {
      element.textContent = key ? t(key, params) : '';
    });
    const snap = app.snapshot();
    const clubName = snap?.config?.clubName || snap?.clubName || '';
    if (screen === 'setup') {
      setKey(title, 'Подключение к клубу');
      setKey(club, connectionText(snap?.connection));
      return;
    }
    if (recovery !== 'none') {
      setKey(title, 'Восстановление пароля');
      setKey(club, recovery === 'phone' ? 'Пришлём код в Telegram на номер вашего аккаунта.' : 'Введите код из Telegram и придумайте новый пароль.');
      setKey($('loginPasswordLabel'), 'Новый пароль');
      setKey(submit, recovery === 'phone' ? 'Получить код в Telegram' : 'Сменить пароль');
    } else {
      setKey(title, registering ? 'Регистрация' : 'Вход');
      if (clubName) setKey(club, 'Клуб: :club', { club: clubName });
      else setText(club, '');
      setKey($('loginPasswordLabel'), 'Пароль');
      setKey(submit, registering ? 'Зарегистрироваться' : 'Войти');
    }
    setKey(registerToggle, registering ? 'Уже есть аккаунт? Войти' : 'Нет аккаунта? Зарегистрироваться');
    setKey(forgotToggle, recovery === 'phone' ? 'Вернуться ко входу' : recovery === 'code' ? 'Запросить новый код' : 'Забыли пароль?');
    renderLanguages();
  };

  const connectionText = (connection) => ({
    CONNECTION_NOT_ENROLLED: 'Введите код подключения из панели клуба: «Компьютеры» → «Подключить ПК».',
    CONNECTION_ENROLLING: 'Подключаем ПК к клубу…',
    CONNECTION_PENDING_APPROVAL: 'ПК ждёт подтверждения в панели клуба.',
    CONNECTION_REVOKED: 'ПК не подключён к клубу. Введите новый код подключения из панели клуба.',
    CONNECTION_CONNECTING: 'Подключаемся к серверу…'
  })[connection] ?? 'Введите код подключения из панели клуба: «Компьютеры» → «Подключить ПК».';

  // ---------------------------------------------------------------------------------------------
  // Вход, регистрация, восстановление пароля (ClubSetupWindow.SubmitAsync, SetRegistering, SetRecovery).
  // ---------------------------------------------------------------------------------------------

  const setMode = () => {
    $('loginNamePanel').hidden = !registering;
    $('loginConfirmPanel').hidden = !(registering || recovery === 'code');
    $('loginCodePanel').hidden = recovery !== 'code';
    $('loginPasswordPanel').hidden = recovery === 'phone';
    registerToggle.hidden = recovery !== 'none';
    setText(error, '');
    applyTexts();
  };

  registerToggle.addEventListener('click', () => {
    if (busy) return;
    registering = !registering;
    showNotice(notice, null);
    setMode();
    (registering ? name : phone).focus();
  });
  forgotToggle.addEventListener('click', () => {
    if (busy) return;
    registering = false;
    recovery = recovery === 'phone' ? 'none' : 'phone';
    showNotice(notice, null);
    setMode();
    phone.focus();
  });

  const fail = (err, fallbackKey) => {
    if (err instanceof Z.BridgeError && err.message) setText(error, err.message);
    else setKey(error, fallbackKey);
  };

  loginForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (busy) return;
    const number = Z.normalizePhone(phone.value);
    if (!number) {
      setKey(error, 'Введите 10 цифр номера после +7.');
      return;
    }
    if (registering && name.value.trim().length < 2) {
      setKey(error, 'Имя — не короче 2 символов.');
      return;
    }
    if ((registering || recovery === 'code') && password.value.length < 6) {
      setKey(error, 'Пароль — не короче 6 символов.');
      return;
    }
    if ((registering || recovery === 'code') && password.value !== confirm.value) {
      setKey(error, 'Пароли не совпадают.');
      return;
    }
    busy = true;
    submit.disabled = true;
    setText(error, '');
    try {
      if (recovery === 'phone') {
        await bridge.call('GuestAuthService/RequestPasswordResetCode', { phone: number });
        recovery = 'code';
        setMode();
        showNotice(notice, 'Код отправлен в Telegram.');
        code.focus();
      } else if (recovery === 'code') {
        await bridge.call('GuestAuthService/ResetPassword', { phone: number, code: code.value.trim(), newPassword: password.value });
        recovery = 'none';
        code.value = '';
        clearPasswords();
        setMode();
        showNotice(notice, 'Пароль изменён. Войдите с новым паролем.');
        password.focus();
      } else if (registering) {
        await bridge.call('GuestAuthService/Register', { name: name.value.trim(), phone: number, password: password.value });
        resetForm();
      } else {
        await bridge.call('GuestAuthService/Login', { phone: number, password: password.value });
        resetForm();
      }
    } catch (err) {
      fail(err, 'Не удалось выполнить вход. Повторите попытку или обратитесь к администратору клуба.');
      if (recovery === 'none') clearPasswords();
    } finally {
      busy = false;
      submit.disabled = false;
    }
  });

  // Гость вошёл (снимок покажет лаунчер) или ушёл: форма пустая, следующий гость начинает с входа.
  const resetForm = () => {
    registering = false;
    recovery = 'none';
    phone.value = '';
    name.value = '';
    code.value = '';
    clearPasswords();
    showNotice(notice, null);
    setMode();
  };

  // ---------------------------------------------------------------------------------------------
  // QR-вход (ClubSetupWindow.RunQrLoginAsync): код живёт до expires_at, потом — новый.
  // ---------------------------------------------------------------------------------------------

  const runQr = async () => {
    if (qrRunning || !bridge.available) return;
    qrRunning = true;
    try {
      while (screen === 'login' && app.snapshot()?.config?.guestFeatures?.qrLogin === true) {
        const created = await bridge.call('GuestAuthService/CreateQrLogin');
        const image = await bridge.call('ui.QrImage', { text: created.url });
        qrImage.src = image.dataUrl;
        qr.hidden = false;
        const result = await bridge.call('GuestAuthService/WatchQrLogin', { loginId: created.loginId });
        if (result.status === 'QR_LOGIN_STATUS_APPROVED') break; // снимок с гостем придёт от службы
      }
    } catch {
      // Выключено в клубе, нет связи — без QR-кода; следующая попытка — при следующем показе экрана входа.
    } finally {
      qr.hidden = true;
      qrImage.removeAttribute('src');
      qrRunning = false;
    }
  };

  // ---------------------------------------------------------------------------------------------
  // Регистрация ПК кодом из панели клуба (agent.Enroll).
  // ---------------------------------------------------------------------------------------------

  setupCode.addEventListener('input', () => {
    const raw = setupCode.value.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 8);
    const formatted = raw.length > 4 ? `${raw.slice(0, 4)}-${raw.slice(4)}` : raw;
    if (formatted !== setupCode.value) setupCode.value = formatted;
  });
  setupForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (busy) return;
    const value = setupCode.value.replace(/[^0-9A-Za-z]/g, '');
    if (value.length < 4) {
      setKey(setupError, 'Введите код подключения из панели клуба.');
      return;
    }
    busy = true;
    setupSubmit.disabled = true;
    setText(setupError, '');
    try {
      await bridge.call('agent.Enroll', { code: setupCode.value.trim() });
      setupCode.value = '';
    } catch (err) {
      if (err instanceof Z.BridgeError && err.message) setText(setupError, err.message);
      else setKey(setupError, 'Не удалось подключить ПК. Проверьте код и подключение к интернету.');
    } finally {
      busy = false;
      setupSubmit.disabled = false;
    }
  });

  // ---------------------------------------------------------------------------------------------
  // PIN сотрудника (Ctrl+Alt+P → событие процесса UI staffPin). Верный PIN — техрежим: служба снимает экран.
  // ---------------------------------------------------------------------------------------------

  const pinDialog = $('pinDialog');
  const pinInput = $('pinInput');
  const pinError = $('pinError');
  const closePin = () => {
    pinInput.value = '';
    setText(pinError, '');
    if (pinDialog.open) pinDialog.close();
  };
  app.onLocal((localEvent) => {
    if (localEvent !== 'staffPin' || pinDialog.open) return;
    pinInput.value = '';
    setText(pinError, '');
    pinDialog.showModal();
    pinInput.focus();
  });
  $('pinCancel').addEventListener('click', closePin);
  pinDialog.addEventListener('cancel', (event) => {
    event.preventDefault();
    closePin();
  });
  $('pinForm').addEventListener('submit', async (event) => {
    event.preventDefault();
    const pin = pinInput.value.trim();
    if (!pin) {
      setKey(pinError, 'Введите PIN-код.');
      return;
    }
    $('pinSubmit').disabled = true;
    setKey(pinError, 'Проверяем…');
    try {
      await bridge.call('agent.VerifyStaffPin', { pin });
      closePin();
    } catch (err) {
      pinInput.value = '';
      if (err instanceof Z.BridgeError && err.message) setText(pinError, err.message);
      else setKey(pinError, 'Не удалось проверить PIN-код.');
    } finally {
      $('pinSubmit').disabled = false;
    }
  });

  // ---------------------------------------------------------------------------------------------
  // Снимок службы: какой экран, тексты подключения, QR.
  // ---------------------------------------------------------------------------------------------

  // Картинка клуба на экране входа вместо стандартной (ShowSignInImage старого ClubSetupWindow): байты — из кэша службы
  // через img.zafiro.invalid (AG-13); нет или не загрузилась — стандартная. signinImageUrl только сообщает, что
  // картинка есть и сменилась (новое имя файла) — оно же меняет адрес, чтобы WebView2 взял новую.
  const lockImage = $('lockImage');
  const standardLockImage = 'gaming-setup.jpg';
  let shownSigninUrl = null;
  lockImage.addEventListener('error', () => {
    if (lockImage.getAttribute('src') !== standardLockImage) lockImage.setAttribute('src', standardLockImage);
  });
  const showSigninImage = (url) => {
    const value = typeof url === 'string' ? url : '';
    if (value === shownSigninUrl) return;
    shownSigninUrl = value;
    const name = value.split('/').pop() ?? '';
    lockImage.setAttribute('src', value && /^[A-Za-z0-9._-]{1,120}$/.test(name)
      ? `https://img.zafiro.invalid/signin?v=${encodeURIComponent(name)}` : standardLockImage);
  };

  app.onSnapshot((snap) => {
    const next = document.body.dataset.screen ?? '';
    const changed = next !== screen;
    screen = next;
    renderPc(snap);
    showSigninImage(snap.config?.signinImageUrl);
    setupForm.hidden = screen !== 'setup';
    loginForm.hidden = screen !== 'login';
    if (screen !== 'login') qr.hidden = true;
    if (changed && screen === 'login') {
      resetForm();
      phone.focus();
    }
    if (changed && screen === 'setup') setupCode.focus();
    setupSubmit.disabled = busy || snap.connection === 'CONNECTION_ENROLLING';
    showNotice(setupNotice, snap.connection === 'CONNECTION_PENDING_APPROVAL' ? 'ПК ждёт подтверждения в панели клуба.' : null);
    applyTexts();
    if (screen === 'login' && !qrRunning) runQr();
  });
  app.onLanguage(applyTexts);
  showPower();
})();
