import { forwardRef, type InputHTMLAttributes } from 'react';

type InputEv = Parameters<NonNullable<InputHTMLAttributes<HTMLInputElement>['onInput']>>[0];
import { reformatPhone } from '../lib/phone';

/**
 * <input class="auth-phone-local" type="tel" …> с маской из resources/js/app.js. Атрибуты — как в шаблонах
 * auth/*.blade.php. Значение в форме — с пробелами и дефисами; при отправке берите phoneDigits().
 */
export const PhoneLocalInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function PhoneLocalInput({ onInput, ...props }, ref) {
    const handleInput = (e: InputEv) => {
      const input = e.currentTarget;
      const { value, cursor } = reformatPhone(input.value, input.selectionStart ?? input.value.length);
      input.value = value;
      input.setSelectionRange(cursor, cursor);
      onInput?.(e);
    };
    return (
      <input
        ref={ref}
        className="auth-phone-local"
        type="tel"
        required
        inputMode="numeric"
        pattern="[0-9]{3} [0-9]{3}-[0-9]{2}-[0-9]{2}"
        maxLength={13}
        placeholder="700 123-45-67"
        autoComplete="tel-national"
        {...props}
        onInput={handleInput}
      />
    );
  },
);
