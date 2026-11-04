import { describe, expect, it } from 'vitest';
import { invitationUrl, parseWorkersSearch, workersHref } from './api';

describe('работники: адреса', () => {
  it('keyset-курсоры в адресе страницы', () => {
    expect(parseWorkersSearch({ cursor: 'abc', before: 'x' })).toEqual({ cursor: 'abc', before: undefined });
    expect(parseWorkersSearch({ before: 'x' })).toEqual({ cursor: undefined, before: 'x' });
    expect(parseWorkersSearch({ cursor: 5 })).toEqual({ cursor: undefined, before: undefined });
    expect(workersHref({ cursor: 'a b' })).toBe('/workers?cursor=a+b');
    expect(workersHref({})).toBe('/workers');
  });

  it('ссылка-приглашение: адрес сайта + путь из ответа', () => {
    expect(invitationUrl('/invitations/tok', 'https://club.example')).toBe('https://club.example/invitations/tok');
  });
});
