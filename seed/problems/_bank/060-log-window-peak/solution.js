/**
 * @param {number[]} counts
 * @param {number} width
 * @returns {number}
 */
function peak_window(counts, width) {
  let current = 0;
  for (let i = 0; i < width; i++) current += counts[i];
  let best = current;
  for (let i = width; i < counts.length; i++) {
    current += counts[i] - counts[i - width];
    if (current > best) best = current;
  }
  return best;
}
