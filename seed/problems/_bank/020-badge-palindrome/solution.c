#include <ctype.h>
#include <stdbool.h>
#include <string.h>

bool is_badge_palindrome(const char *badge) {
    int i = 0, j = (int) strlen(badge) - 1;
    while (i < j) {
        while (i < j && !isalnum((unsigned char) badge[i])) i++;
        while (i < j && !isalnum((unsigned char) badge[j])) j--;
        if (tolower((unsigned char) badge[i]) != tolower((unsigned char) badge[j])) return false;
        i++;
        j--;
    }
    return true;
}
