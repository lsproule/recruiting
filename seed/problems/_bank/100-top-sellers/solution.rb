def top_sellers(sales, k)
  sales.keys.sort_by { |name| [-sales[name], name] }.first(k)
end
