import type { HTMLAttributes } from 'react';

/**
 * Карточка подраздела настроек — resources/views/components/settings/card.blade.php (`<x-settings.card>`):
 * id карточки — якорь в левом меню; свои классы добавляются к базовым ($attributes->class([...])).
 */
export function SettingsCard({ className, children, ...rest }: HTMLAttributes<HTMLElement>) {
  const base = 'scroll-mt-6 bg-white dark:bg-gray-900 rounded-2xl border border-gray-200 dark:border-gray-800 p-6 animate-slide shadow-sm animation-delay-100';
  return (
    <section className={className ? `${base} ${className}` : base} {...rest}>
      {children}
    </section>
  );
}
