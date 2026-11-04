import { describe, expect, it } from 'vitest';
import { activityLogsHref, numberFormat, parseActivityLogsSearch } from './api';

describe('журнал действий: адрес и числа', () => {
  it('number_format($n, 0, ",", " ")', () => {
    expect(numberFormat(0)).toBe('0');
    expect(numberFormat(1234567)).toBe('1 234 567');
  });

  it('фильтры и курсор сохраняются в адресе (withQueryString)', () => {
    const s = parseActivityLogsSearch({ event: 'cash.income', user_id: 5, search: '', cursor: 'abc', junk: 'x' });
    expect(s).toEqual({ event: 'cash.income', user_id: '5', cursor: 'abc' });
    expect(activityLogsHref(s)).toBe('/activity-logs?event=cash.income&user_id=5&cursor=abc');
    expect(activityLogsHref({})).toBe('/activity-logs');
  });
});
