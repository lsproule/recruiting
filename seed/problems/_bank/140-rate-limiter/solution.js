/**
 * @param {number[]} timestamps
 * @param {number} limit
 * @param {number} window
 * @returns {number}
 */
function dropped_requests(timestamps, limit, window) {
  const accepted = [];
  let head = 0, dropped = 0;
  for (const t of timestamps) {
    while (head < accepted.length && accepted[head] <= t - window) head++;
    if (accepted.length - head < limit) accepted.push(t); else dropped++;
  }
  return dropped;
}
