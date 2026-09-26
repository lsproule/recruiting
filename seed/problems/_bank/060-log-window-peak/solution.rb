def peak_window(counts, width)
  current = counts.take(width).sum
  best = current
  (width...counts.length).each do |i|
    current += counts[i] - counts[i - width]
    best = current if current > best
  end
  best
end
