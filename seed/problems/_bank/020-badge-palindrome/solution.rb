def is_badge_palindrome(badge)
  keep = badge.downcase.gsub(/[^a-z0-9]/, "")
  keep == keep.reverse
end
