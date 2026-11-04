import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';

/*
 * Якоря подразделов открытой страницы настроек — resources/views/settings/anchors.blade.php ($settingsAnchors:
 * id => название) и скрипт settings/layout.blade.php: текущим отмечается раздел, верх которого выше 120px
 * (aria-current), при прокрутке — пересчёт (scroll на document, capture).
 */
export function SettingsAnchors({ anchors }: { anchors: ReadonlyArray<readonly [id: string, label: string]> }) {
  const { t } = useTranslation();
  const nav = useRef<HTMLElement>(null);

  useEffect(() => {
    const links = [...(nav.current?.querySelectorAll<HTMLAnchorElement>('a') ?? [])];
    const mark = () => {
      const sections = links.map((link) => document.getElementById(link.hash.slice(1))).filter((s): s is HTMLElement => s !== null);
      const current = sections.filter((s) => s.getBoundingClientRect().top <= 120).pop() ?? sections[0];
      links.forEach((link) => link.setAttribute('aria-current', String(link.hash === '#' + current?.id)));
    };
    document.addEventListener('scroll', mark, { capture: true, passive: true });
    const raf = requestAnimationFrame(mark);
    return () => {
      document.removeEventListener('scroll', mark, { capture: true });
      cancelAnimationFrame(raf);
    };
  }, [anchors]);

  return (
    <nav ref={nav} className="mb-1 ml-7 space-y-0.5 border-l border-gray-200 pl-3 dark:border-gray-700" aria-label={t('Разделы страницы')} data-settings-anchors>
      {anchors.map(([id, label]) => (
        <a
          key={id}
          href={`#${id}`}
          className="block rounded-lg px-2 py-1.5 text-sm text-gray-600 transition hover:bg-gray-100 hover:text-gray-900 aria-[current=true]:font-semibold aria-[current=true]:text-gray-900 dark:text-gray-400 dark:hover:bg-gray-800 dark:hover:text-white dark:aria-[current=true]:text-white"
        >
          {label}
        </a>
      ))}
    </nav>
  );
}
