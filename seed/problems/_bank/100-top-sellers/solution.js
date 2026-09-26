/**
 * @param {Object<string, number>} sales
 * @param {number} k
 * @returns {string[]}
 */
function top_sellers(sales, k) {
  const names = Object.keys(sales);
  names.sort((a, b) => sales[b] - sales[a] || (a < b ? -1 : a > b ? 1 : 0));
  return names.slice(0, k);
}
