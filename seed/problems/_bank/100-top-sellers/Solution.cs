using System;
using System.Collections.Generic;
using System.Linq;

class Solution {
    public static string[] top_sellers(Dictionary<string, int> sales, int k) {
        return sales.Keys
            .OrderByDescending(name => sales[name])
            .ThenBy(name => name, StringComparer.Ordinal)
            .Take(k)
            .ToArray();
    }
}
