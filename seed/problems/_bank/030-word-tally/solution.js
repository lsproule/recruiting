/**
 * @param {string[]} words
 * @returns {Object<string, number>}
 */
function word_tally(words) {
  const counts = {};
  for (const w of words) counts[w] = (counts[w] ?? 0) + 1;
  return counts;
}
