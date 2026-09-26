def min_coins(coins, amount)
  unreachable = amount + 1
  best = Array.new(amount + 1, unreachable)
  best[0] = 0
  (1..amount).each do |a|
    coins.each do |c|
      best[a] = best[a - c] + 1 if c <= a && best[a - c] + 1 < best[a]
    end
  end
  best[amount] == unreachable ? -1 : best[amount]
end
