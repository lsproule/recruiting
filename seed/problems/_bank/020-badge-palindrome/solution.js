/**
 * @param {string} badge
 * @returns {boolean}
 */
function is_badge_palindrome(badge) {
  const keep = badge.toLowerCase().replace(/[^a-z0-9]/g, "");
  for (let i = 0, j = keep.length - 1; i < j; i++, j--) {
    if (keep[i] !== keep[j]) return false;
  }
  return true;
}
