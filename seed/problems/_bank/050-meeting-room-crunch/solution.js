/**
 * @param {number[]} starts
 * @param {number[]} ends
 * @returns {number}
 */
function rooms_needed(starts, ends) {
  const s = [...starts].sort((a, b) => a - b);
  const e = [...ends].sort((a, b) => a - b);
  let rooms = 0, best = 0, j = 0;
  for (const start of s) {
    while (j < e.length && e[j] <= start) {
      rooms--;
      j++;
    }
    rooms++;
    if (rooms > best) best = rooms;
  }
  return best;
}
