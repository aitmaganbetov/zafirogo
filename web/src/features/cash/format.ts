/*
 * Суммы корзины кассы — как старый JS кассы: new Intl.NumberFormat('ru-RU').format(value) + ' ₸' (value в тенге;
 * дробная часть — до 2 знаков без хвостовых нулей). Здесь — из тиынов, без float.
 */
const intl = new Intl.NumberFormat('ru-RU');

export function posMoney(tiyn: number): string {
  if (!Number.isSafeInteger(tiyn)) throw new RangeError(`posMoney: not an integer tiyn amount: ${tiyn}`);
  const neg = tiyn < 0;
  const abs = Math.abs(tiyn);
  const whole = intl.format(Math.floor(abs / 100));
  const frac = abs % 100;
  const fracStr = frac === 0 ? '' : ',' + String(frac).padStart(2, '0').replace(/0$/, '');
  return (neg ? '-' : '') + whole + fracStr + ' ₸';
}

/** Иконка категории товара (App\Support\CategoryIcon::for()): по словам в названии, иначе коробка. */
const ICONS: readonly (readonly [string, readonly string[]])[] = [
  ['fa-mug-hot', ['кофе', 'чай', 'coffee', 'tea', 'шай', 'кофе']],
  ['fa-bolt', ['энерг', 'energy']],
  ['fa-bottle-water', ['напит', 'сусын', 'drink', 'вод', 'water', 'сок', 'juice', 'кола', 'cola', 'газир']],
  ['fa-cookie-bite', ['снэк', 'снек', 'снак', 'snack', 'чипс', 'chips', 'печен', 'орех']],
  ['fa-candy-cane', ['слад', 'конфет', 'шокол', 'candy', 'sweet', 'тәтті']],
  ['fa-burger', ['еда', 'food', 'тағам', 'пицц', 'pizza', 'бургер', 'burger', 'хот-дог', 'hot dog', 'лапш', 'обед']],
  ['fa-ice-cream', ['морож', 'ice cream', 'балмұздақ']],
  ['fa-gamepad', ['консол', 'console', 'playstation', 'xbox', 'игр', 'game', 'ойын']],
  ['fa-headphones', ['аксесс', 'accessor', 'наушн', 'headphone', 'гарнитур']],
  ['fa-smoking', ['табак', 'кальян', 'вейп', 'vape', 'hookah']],
  ['fa-gift', ['подар', 'gift', 'сертиф', 'сыйлық']],
];

export function categoryIcon(name: string | null | undefined): string {
  const n = (name ?? '').toLowerCase();
  for (const [icon, words] of ICONS) {
    if (words.some((w) => n.includes(w))) return icon;
  }
  return 'fa-box';
}
