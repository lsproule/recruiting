def deploy_order(services, deps)
  indegree = services.to_h { |s| [s, 0] }
  after = services.to_h { |s| [s, []] }
  deps.each do |before, later|
    after[before] << later
    indegree[later] += 1
  end
  ready = services.select { |s| indegree[s].zero? }.sort
  order = []
  until ready.empty?
    s = ready.shift
    order << s
    after[s].each do |t|
      indegree[t] -= 1
      next unless indegree[t].zero?
      at = ready.bsearch_index { |x| x >= t } || ready.length
      ready.insert(at, t)
    end
  end
  order.length == services.length ? order : []
end
