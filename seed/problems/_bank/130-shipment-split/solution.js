/**
 * @param {number[]} weights
 * @param {number} trucks
 * @returns {number}
 */
function min_max_load(weights, trucks) {
  const fits = (capacity) => {
    let used = 1, load = 0;
    for (const w of weights) {
      if (load + w > capacity) {
        used++;
        load = w;
      } else {
        load += w;
      }
    }
    return used <= trucks;
  };
  let lo = Math.max(...weights), hi = weights.reduce((a, b) => a + b, 0);
  while (lo < hi) {
    const mid = Math.floor((lo + hi) / 2);
    if (fits(mid)) hi = mid; else lo = mid + 1;
  }
  return lo;
}
