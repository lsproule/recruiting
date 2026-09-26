def voucher_pair(amounts, target)
  seen = {}
  amounts.each_with_index do |a, j|
    return [seen[target - a], j] if seen.key?(target - a)
    seen[a] = j unless seen.key?(a)
  end
  []
end
