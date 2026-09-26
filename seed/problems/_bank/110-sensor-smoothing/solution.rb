def moving_average(readings, width)
  total = readings.take(width).sum
  out = [total / width]
  (width...readings.length).each do |i|
    total += readings[i] - readings[i - width]
    out << total / width
  end
  out
end
