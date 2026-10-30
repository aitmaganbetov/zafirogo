import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { SimplePagination } from './SimplePagination';

describe('SimplePagination (simple-tailwind.blade.php)', () => {
  it('нет страниц — ничего', () => {
    const { container } = render(<SimplePagination prevHref={null} nextHref={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('первая страница: «назад» неактивно, «вперёд» — ссылка', () => {
    render(<SimplePagination prevHref={null} nextHref="/users?after=25" />);
    const nav = screen.getByRole('navigation');
    expect(nav).toHaveClass('flex', 'gap-2', 'items-center', 'justify-between');
    expect(nav.querySelector('span')).toHaveClass('cursor-not-allowed');
    expect(nav.querySelector('a')).toHaveAttribute('href', '/users?after=25');
    expect(nav.querySelector('a')).toHaveAttribute('rel', 'next');
  });

  it('последняя страница: «назад» — ссылка, «вперёд» неактивно', () => {
    render(<SimplePagination prevHref="/users?before=26" nextHref={null} />);
    const nav = screen.getByRole('navigation');
    expect(nav.querySelector('a')).toHaveAttribute('rel', 'prev');
    expect(nav.querySelectorAll('span.cursor-not-allowed')).toHaveLength(1);
  });
});
