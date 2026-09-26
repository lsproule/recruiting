class Solution {
    public static boolean is_badge_palindrome(String badge) {
        int i = 0, j = badge.length() - 1;
        while (i < j) {
            while (i < j && !Character.isLetterOrDigit(badge.charAt(i))) i++;
            while (i < j && !Character.isLetterOrDigit(badge.charAt(j))) j--;
            if (Character.toLowerCase(badge.charAt(i)) != Character.toLowerCase(badge.charAt(j))) return false;
            i++;
            j--;
        }
        return true;
    }
}
