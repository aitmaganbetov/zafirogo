import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { makeMissingKeyHandler } from './missing';

/*
 * i18next: ключ — русский текст, как __('Текст') в Laravel (решение Q-065). Переводы загружаются с сервера
 * (GET /api/i18n/{locale}, i18n/load.ts) — их правят в суперадминке, в бандле их нет. Подстановки {{name}},
 * множественное число — t('{{count}} минута|{{count}} минуты|{{count}} минут', { count }) (сервер отдаёт формы
 * _one/_few/_many/_other). keySeparator/nsSeparator выключены: в ключах есть точки и двоеточия.
 */
export const SUPPORTED_LANGUAGES = ['ru', 'kk', 'en'] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];
/** LocaleResolver::DEFAULT_CLUB_LOCALE. */
export const DEFAULT_LANGUAGE: Language = 'ru';
/** LocaleResolver::DEFAULT_GUEST_LOCALE. */
export const DEFAULT_GUEST_LANGUAGE: Language = 'en';

void i18n.use(initReactI18next).init({
  resources: {},
  partialBundledLanguages: true,
  lng: DEFAULT_LANGUAGE,
  // Нет перевода — показывается ключ (русский текст), как в Laravel.
  fallbackLng: false,
  keySeparator: false,
  nsSeparator: false,
  returnEmptyString: false,
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
  // Нет перевода: форма trans_choice по числу (а не все формы через «|») и подстановки в ключ (CM-11, missing.ts).
  parseMissingKeyHandler: makeMissingKeyHandler((text, params) =>
    i18n.services.interpolator.interpolate(text, params, i18n.language, {}),
  ),
});

export default i18n;
