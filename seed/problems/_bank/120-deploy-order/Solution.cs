using System;
using System.Collections.Generic;

class Solution {
    public static string[] deploy_order(string[] services, string[][] deps) {
        var indegree = new Dictionary<string, int>();
        var after = new Dictionary<string, List<string>>();
        foreach (var s in services) {
            indegree[s] = 0;
            after[s] = new List<string>();
        }
        foreach (var d in deps) {
            after[d[0]].Add(d[1]);
            indegree[d[1]]++;
        }
        var ready = new PriorityQueue<string, string>(StringComparer.Ordinal);
        foreach (var s in services) if (indegree[s] == 0) ready.Enqueue(s, s);
        var order = new List<string>();
        while (ready.Count > 0) {
            var s = ready.Dequeue();
            order.Add(s);
            foreach (var t in after[s]) {
                if (--indegree[t] == 0) ready.Enqueue(t, t);
            }
        }
        return order.Count == services.Length ? order.ToArray() : new string[0];
    }
}
