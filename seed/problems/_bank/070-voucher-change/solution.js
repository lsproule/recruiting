/**
 * @param {number[]} coins
 * @param {number} amount
 * @returns {number}
 */
function min_coins(coins, amount) {
  const unreachable = amount + 1;
  const best = new Array(amount + 1).fill(unreachable);
  best[0] = 0;
  for (let a = 1; a <= amount; a++) {
    for (const c of coins) {
      if (c <= a && best[a - c] + 1 < best[a]) best[a] = best[a - c] + 1;
    }
  }
  return best[amount] === unreachable ? -1 : best[amount];
}
