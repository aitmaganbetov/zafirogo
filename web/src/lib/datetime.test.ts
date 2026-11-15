import { describe, expect, it } from 'vitest';
import { addDays, clubDate, clubLocalToISO, formatDate, serverNow } from './datetime';

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

// FE-13: срок ключа образа вводится в поясе клуба, пояс браузера не влияет.
describe('clubLocalToISO: время на часах клуба → UTC', () => {
  it('пояс клуба, а не браузера', () => {
    expect(clubLocalToISO('2026-11-20T18:30', 'Asia/Almaty')).toBe('2026-11-20T13:30:00.000Z');
    expect(clubLocalToISO('2026-11-20T18:30', 'UTC')).toBe('2026-11-20T18:30:00.000Z');
    expect(clubLocalToISO('2026-01-01T00:00', 'America/New_York')).toBe('2026-01-01T05:00:00.000Z');
    expect(clubLocalToISO('2026-07-01T00:00:30', 'America/New_York')).toBe('2026-07-01T04:00:30.000Z');
  });
  it('обратимо с formatDate в том же поясе', () => {
    for (const tz of ['Asia/Almaty', 'Europe/Berlin', 'America/New_York', 'Asia/Kolkata']) {
      const iso = clubLocalToISO('2026-03-29T12:15', tz);
      expect(iso && formatDate(iso, tz, 'Y-m-d H:i')).toBe('2026-03-29 12:15');
    }
  });
  it('переход на летнее время: несуществующее время сдвигается вперёд', () => {
    // Берлин, 29.03.2026: 02:00 → 03:00.
    expect(clubLocalToISO('2026-03-29T02:30', 'Europe/Berlin')).toBe('2026-03-29T01:30:00.000Z');
  });
  it('пусто или мусор — null', () => {
    expect(clubLocalToISO('', 'UTC')).toBeNull();
    expect(clubLocalToISO('завтра', 'UTC')).toBeNull();
  });
});
