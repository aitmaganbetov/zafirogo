/**
 * Элементы пагинации — Illuminate\Pagination\UrlWindow::get() (onEachSide = 3) и
 * LengthAwarePaginator::elements(): номера страниц и '...' между окнами.
 */
export function paginationElements(current: number, lastPage: number, onEachSide = 3): (number | '...')[] {
  const range = (a: number, b: number) => Array.from({ length: Math.max(0, b - a + 1) }, (_, i) => a + i);
  if (lastPage < onEachSide * 2 + 8) return range(1, lastPage); // getSmallSlider()
  const window = onEachSide + 4;
  let first: number[];
  let slider: number[] | null = null;
  let last: number[];
  if (current <= window) {
    // getSliderTooCloseToBeginning()
    first = range(1, window + onEachSide);
    last = range(lastPage - 1, lastPage);
  } else if (current > lastPage - window) {
    // getSliderTooCloseToEnding()
    first = range(1, 2);
    last = range(lastPage - (window + (onEachSide - 1)), lastPage);
  } else {
    // getFullSlider()
    first = range(1, 2);
    slider = range(current - onEachSide, current + onEachSide);
    last = range(lastPage - 1, lastPage);
  }
  const out: (number | '...')[] = [...first, '...'];
  if (slider) out.push(...slider, '...');
  out.push(...last);
  return out;
}
