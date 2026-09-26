#include <cctype>
#include <string>

bool is_badge_palindrome(std::string badge) {
    int i = 0, j = static_cast<int>(badge.size()) - 1;
    while (i < j) {
        while (i < j && !std::isalnum(static_cast<unsigned char>(badge[i]))) i++;
        while (i < j && !std::isalnum(static_cast<unsigned char>(badge[j]))) j--;
        if (std::tolower(static_cast<unsigned char>(badge[i])) != std::tolower(static_cast<unsigned char>(badge[j]))) return false;
        i++;
        j--;
    }
    return true;
}
