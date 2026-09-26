class Solution {
    public static bool is_badge_palindrome(string badge) {
        int i = 0, j = badge.Length - 1;
        while (i < j) {
            while (i < j && !char.IsLetterOrDigit(badge[i])) i++;
            while (i < j && !char.IsLetterOrDigit(badge[j])) j--;
            if (char.ToLowerInvariant(badge[i]) != char.ToLowerInvariant(badge[j])) return false;
            i++;
            j--;
        }
        return true;
    }
}
