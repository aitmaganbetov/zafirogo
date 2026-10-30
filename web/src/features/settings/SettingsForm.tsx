import { useMutation, useQueryClient } from '@tanstack/react-query';
import type { FormEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { formToBody, saveSection, type SettingsSection } from './api';

/*
 * Общая форма секции настроек — @section('settings-content') в settings/layout.blade.php: POST /settings/save со
 * скрытым section и кнопкой «Сохранить». Сохраняет PUT /api/settings/sections/{section} с полями формы.
 */
export function SettingsForm({ section, children, readOnly, onDone, onError }: {
  readonly section: SettingsSection;
  readonly children: ReactNode;
  readonly readOnly: boolean;
  readonly onDone: (message: string) => void;
  readonly onError: (e: unknown) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => saveSection(section, body),
    onSuccess: (r) => {
      onDone(r.message);
      return qc.invalidateQueries({ queryKey: ['settings'] });
    },
    onError,
  });
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (readOnly) return;
    save.mutate(formToBody(e.currentTarget));
  };
  return (
    <form action="/settings/save" method="POST" className="space-y-6" onSubmit={submit}>
      <input type="hidden" name="section" value={section} />
      {children}

      <div className="flex justify-end">
        <button type="submit" className="brand-button rounded-xl px-5 py-2 text-sm transition" disabled={readOnly || save.isPending}>
          {t('Сохранить')}
        </button>
      </div>
    </form>
  );
}

