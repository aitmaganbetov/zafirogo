import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Pagination } from './Pagination';
import { paginationElements } from './paginationElements';

describe('UrlWindow (onEachSide = 3)', () => {
  it('мало страниц — все', () => {
    expect(paginationElements(1, 13)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13]);
  });
  it('у начала', () => {
    expect(paginationElements(3, 20)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, '...', 19, 20]);
  });
  it('в середине', () => {
    expect(paginationElements(10, 20)).toEqual([1, 2, '...', 7, 8, 9, 10, 11, 12, 13, '...', 19, 20]);
  });
  it('у конца', () => {
    expect(paginationElements(18, 20)).toEqual([1, 2, '...', 11, 12, 13, 14, 15, 16, 17, 18, 19, 20]);
  });
});

describe('<Pagination> — tailwind.blade.php Laravel', () => {
  it('одна страница — ничего', () => {
    const { container } = render(<Pagination page={1} perPage={25} total={10} count={10} hrefFor={(p) => `?page=${p}`} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('текущая страница, ссылки и «Showing … of … results»', () => {
    render(<Pagination page={2} perPage={25} total={60} count={25} hrefFor={(p) => `/users?page=${p}`} />);
    expect(screen.getByRole('navigation')).toHaveAttribute('aria-label', 'Pagination Navigation');
    expect(screen.getByText('2', { selector: 'span' }).parentElement).toHaveAttribute('aria-current', 'page');
    expect(screen.getByLabelText('Go to page 3')).toHaveAttribute('href', '/users?page=3');
    const p = screen.getByText(/Showing/);
    expect(p.textContent?.replace(/\s+/g, ' ')).toBe('Showing 26 to 50 of 60 results');
    expect(screen.getAllByRole('link', { name: 'pagination.previous' })[0]).toHaveAttribute('href', '/users?page=1');
  });
});
