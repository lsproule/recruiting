/**
 * @param {string[]} services
 * @param {string[][]} deps
 * @returns {string[]}
 */
function deploy_order(services, deps) {
  const indegree = new Map(services.map((s) => [s, 0]));
  const after = new Map(services.map((s) => [s, []]));
  for (const [before, then] of deps) {
    after.get(before).push(then);
    indegree.set(then, indegree.get(then) + 1);
  }
  // A sorted array stands in for a heap: ready is small and pops from the front.
  const ready = services.filter((s) => indegree.get(s) === 0).sort();
  const order = [];
  while (ready.length > 0) {
    const s = ready.shift();
    order.push(s);
    for (const t of after.get(s)) {
      indegree.set(t, indegree.get(t) - 1);
      if (indegree.get(t) === 0) {
        let lo = 0, hi = ready.length;
        while (lo < hi) {
          const mid = (lo + hi) >> 1;
          if (ready[mid] < t) lo = mid + 1; else hi = mid;
        }
        ready.splice(lo, 0, t);
      }
    }
  }
  return order.length === services.length ? order : [];
}
