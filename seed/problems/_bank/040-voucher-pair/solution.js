/**
 * @param {number[]} amounts
 * @param {number} target
 * @returns {number[]}
 */
function voucher_pair(amounts, target) {
  const seen = new Map();
  for (let j = 0; j < amounts.length; j++) {
    const want = target - amounts[j];
    if (seen.has(want)) return [seen.get(want), j];
    if (!seen.has(amounts[j])) seen.set(amounts[j], j);
  }
  return [];
}
