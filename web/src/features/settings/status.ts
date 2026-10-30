import { useCallback, useState } from 'react';

/*
 * Состояние страницы настроек после действия — как редирект back()->with('status') / withErrors() старого кода:
 * текст status (блок над карточками) или ошибка (у своей формы). Страница прокручивается наверх, как после
 * перезагрузки.
 */
export function useSettingsResult() {
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<{ readonly form: string; readonly e: unknown } | null>(null);
  const done = useCallback((message: string) => {
    setError(null);
    setStatus(message);
    window.scrollTo?.({ top: 0 });
  }, []);
  const fail = useCallback((form: string) => (e: unknown) => {
    setStatus(null);
    setError({ form, e });
  }, []);
  const errorOf = (form: string): unknown => (error?.form === form ? error.e : null);
  return { status, done, fail, errorOf };
}
