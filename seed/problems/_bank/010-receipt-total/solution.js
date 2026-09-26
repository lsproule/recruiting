/**
 * @param {number[]} quantities
 * @param {number[]} prices
 * @returns {number}
 */
function receipt_total(quantities, prices) {
  let total = 0;
  for (let i = 0; i < quantities.length; i++) total += quantities[i] * prices[i];
  return total;
}
