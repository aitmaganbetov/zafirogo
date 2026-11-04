import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ConfirmForm } from './ConfirmForm';
import { FieldError } from './FieldError';
import { PhoneLocalInput } from './PhoneLocalInput';
import { SettingsCard } from './SettingsCard';

describe('общие компоненты', () => {
  it('SettingsCard: базовые классы <x-settings.card> + свои', () => {
    render(<SettingsCard id="club" className="mt-4">x</SettingsCard>);
    const el = screen.getByText('x');
    expect(el.tagName).toBe('SECTION');
    expect(el).toHaveAttribute('id', 'club');
    expect(el.className).toBe(
      'scroll-mt-6 bg-white dark:bg-gray-900 rounded-2xl border border-gray-200 dark:border-gray-800 p-6 animate-slide shadow-sm animation-delay-100 mt-4',
    );
  });

  it('ConfirmForm: действие только после «ОК»', () => {
    const ok = vi.fn();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(
      <ConfirmForm message="Удалить?" onConfirm={ok}>
        <button type="submit">Удалить</button>
      </ConfirmForm>,
    );
    fireEvent.click(screen.getByText('Удалить'));
    expect(ok).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText('Удалить'));
    expect(ok).toHaveBeenCalledTimes(1);
    expect(confirm).toHaveBeenCalledWith('Удалить?');
  });

  it('FieldError: класс шаблона и ключ перевода', () => {
    const { container, rerender } = render(<FieldError message={null} className="text-red-600" />);
    expect(container).toBeEmptyDOMElement();
    rerender(<FieldError message="Имя обязательно." className="text-red-600" />);
    expect(screen.getByText('Имя обязательно.')).toHaveClass('text-red-600');
  });

  it('PhoneLocalInput: маска при вводе и атрибуты шаблона', () => {
    render(<PhoneLocalInput id="phone" />);
    const input = screen.getByRole('textbox') as HTMLInputElement;
    expect(input).toHaveClass('auth-phone-local');
    expect(input).toHaveAttribute('pattern', '[0-9]{3} [0-9]{3}-[0-9]{2}-[0-9]{2}');
    input.value = '7011234567';
    fireEvent.input(input);
    expect(input.value).toBe('701 123-45-67');
  });
});
