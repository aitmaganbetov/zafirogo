import { createContext, useContext } from 'react';

/** Flash кабинета: session('status') и $errors->first() в layouts/superadmin.blade.php. */
export interface SuperadminFlash {
  readonly status: (message: string) => void;
  readonly error: (message: string) => void;
}

export const FlashContext = createContext<SuperadminFlash>({ status: () => undefined, error: () => undefined });

export function useSuperadminFlash(): SuperadminFlash {
  return useContext(FlashContext);
}
