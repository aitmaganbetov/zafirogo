import { describe, expect, it } from 'vitest';
import { addDays, clubDate, formatDate, serverNow } from './datetime';

describe('даты в поясе клуба', () => {
  const at = '2026-10-30T19:05:09Z';
  it('d.m.Y H:i в Asia/Almaty (UTC+5)', () => {
    expect(formatDate(at, 'Asia/Almaty')).toBe('31.10.2026 00:05');
  });
  it('токены PHP и экранирование', () => {
    expect(formatDate(at, 'UTC', 'j.n.y G:i:s \\d')).toBe('30.10.26 19:05:09 d');
  });
  it('локальная дата клуба', () => {
    expect(clubDate(at, 'Asia/Almaty')).toBe('2026-10-31');
    expect(clubDate(at, 'Europe/Moscow')).toBe('2026-10-30');
  });
  it('addDays через границу месяца', () => {
    expect(addDays('2026-10-31', 1)).toBe('2026-11-01');
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28');
  });
  it('поправка часов', () => {
    expect(serverNow(1_000_000, 900_000, 950_000)).toBe(1_050_000);
  });
});
