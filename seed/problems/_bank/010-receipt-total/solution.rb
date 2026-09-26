def receipt_total(quantities, prices)
  quantities.zip(prices).sum { |q, p| q * p }
end
