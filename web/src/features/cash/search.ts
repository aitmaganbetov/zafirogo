/** ?page= Laravel: целое 1…100000, иначе 1 (как paginate()). */
export function pageParam(v: unknown): number {
  const n = Number(v);
  return Number.isInteger(n) && n >= 1 && n <= 100000 ? n : 1;
}
