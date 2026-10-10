// Width of a fixed table actions column holding `count` icon buttons. Mirrors
// the .row-actions metrics in styles/index.css (26px per button, 2px between,
// 24px of cell padding) — change both together. The floor keeps the column
// wide enough for its own title, so a one-button (or empty) table never
// clips "Actions".
const MIN_WIDTH = 76;

export function actionsWidth(count) {
  const n = Math.max(1, count);
  return Math.max(MIN_WIDTH, n * 26 + (n - 1) * 2 + 24);
}
