def min_max_load(weights, trucks)
  fits = lambda do |capacity|
    used = 1
    load = 0
    weights.each do |w|
      if load + w > capacity
        used += 1
        load = w
      else
        load += w
      end
    end
    used <= trucks
  end
  lo = weights.max
  hi = weights.sum
  while lo < hi
    mid = (lo + hi) / 2
    if fits.call(mid)
      hi = mid
    else
      lo = mid + 1
    end
  end
  lo
end
