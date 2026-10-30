import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import i18n from './i18n';
import { App } from './App';

describe('App', () => {
  it('рендерит каркас панели с заглушкой страницы', () => {
    render(<App />);
    expect(screen.getByRole('heading', { level: 1, name: 'Zafiro' })).toBeInTheDocument();
    expect(screen.getByText('Сервер работает')).toBeInTheDocument();
  });

  it('переключает язык', async () => {
    await i18n.changeLanguage('en');
    render(<App />);
    expect(screen.getByText('Server is running')).toBeInTheDocument();
    await i18n.changeLanguage('ru');
  });
});
