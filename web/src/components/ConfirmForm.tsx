import type { FormEvent, ReactNode } from 'react';

/**
 * Форма с подтверждением — `<form … onsubmit="return confirm(@js(__('…')))">` старых шаблонов (удаление, возврат,
 * отмена брони). `message` — уже переведённый текст. onConfirm вызывается только после «ОК».
 */
export function ConfirmForm({
  message,
  onConfirm,
  className,
  children,
}: {
  message: string;
  onConfirm: () => void;
  className?: string;
  children: ReactNode;
}) {
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(message)) onConfirm();
  };
  return (
    <form method="POST" className={className} onSubmit={submit}>
      {children}
    </form>
  );
}
