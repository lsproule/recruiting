def is_badge_palindrome(badge: str) -> bool:
    i, j = 0, len(badge) - 1
    while i < j:
        while i < j and not badge[i].isalnum():
            i += 1
        while i < j and not badge[j].isalnum():
            j -= 1
        if badge[i].lower() != badge[j].lower():
            return False
        i += 1
        j -= 1
    return True
