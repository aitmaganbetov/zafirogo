import { useEffect } from 'react';
import { APP_NAME } from '../layouts/adminSession';

/** <title>@yield('title') · Zafiro</title> страниц вне панели (вход, ошибки). Текст — уже переведённый. */
export function useDocumentTitle(title: string): void {
  useEffect(() => {
    document.title = `${title} · ${APP_NAME}`;
  }, [title]);
}
