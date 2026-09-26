def rooms_needed(starts, ends)
  s = starts.sort
  e = ends.sort
  rooms = best = 0
  j = 0
  s.each do |start|
    while j < e.length && e[j] <= start
      rooms -= 1
      j += 1
    end
    rooms += 1
    best = rooms if rooms > best
  end
  best
end
