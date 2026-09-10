import { render, screen } from '@testing-library/svelte';
import App from './App.svelte';

describe('App', () => {
  it('renders hello devm', () => {
    render(App);
    expect(screen.getByText('hello devm')).toBeTruthy();
  });
});
