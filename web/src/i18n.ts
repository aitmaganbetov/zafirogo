import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

// Ключи переводов — русский текст (как в Laravel: __('Текст')). Для ru перевод не нужен:
// при отсутствии ключа i18next возвращает сам ключ. Переводы kk/en позже загружаются
// с сервера (редактируются в суперадминке), здесь — только стартовый минимум.
export const SUPPORTED_LANGUAGES = ['ru', 'kk', 'en'] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];
export const DEFAULT_LANGUAGE: Language = 'ru';

const resources = {
  ru: { translation: {} },
  kk: {
    translation: {
      'Панель управления': 'Басқару панелі',
      'Сервер работает': 'Сервер жұмыс істеп тұр',
    },
  },
  en: {
    translation: {
      'Панель управления': 'Dashboard',
      'Сервер работает': 'Server is running',
    },
  },
};

void i18n.use(initReactI18next).init({
  resources,
  lng: DEFAULT_LANGUAGE,
  fallbackLng: DEFAULT_LANGUAGE,
  supportedLngs: [...SUPPORTED_LANGUAGES],
  keySeparator: false,
  nsSeparator: false,
  returnEmptyString: false,
  interpolation: { escapeValue: false },
});

export default i18n;
