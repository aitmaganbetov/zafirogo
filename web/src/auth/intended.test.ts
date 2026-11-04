import { beforeEach, describe, expect, it } from 'vitest';
import { rememberIntended, takeIntended } from './intended';

// SEC-020: адрес после входа — только путь этого сайта.
describe('takeIntended', () => {
  beforeEach(() => sessionStorage.clear());

  it.each([
    ['/cash-register?tab=1', '/cash-register?tab=1'],
    ['/pc-login/abc', '/pc-login/abc'],
    ['//evil.example/x', '/dashboard'],
    ['/\\evil.example/x', '/dashboard'],
    ['/\\/evil.example', '/dashboard'],
    ['/\t/evil.example', '/dashboard'],
    ['https://evil.example/', '/dashboard'],
    ['javascript:alert(1)', '/dashboard'],
  ])('%s → %s', (stored, want) => {
    rememberIntended(stored);
    expect(takeIntended('/dashboard')).toBe(want);
  });
});
