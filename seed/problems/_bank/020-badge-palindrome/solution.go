package main

func is_badge_palindrome(badge string) bool {
	keep := make([]byte, 0, len(badge))
	for i := 0; i < len(badge); i++ {
		c := badge[i]
		switch {
		case c >= 'A' && c <= 'Z':
			keep = append(keep, c+'a'-'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			keep = append(keep, c)
		}
	}
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		if keep[i] != keep[j] {
			return false
		}
	}
	return true
}
