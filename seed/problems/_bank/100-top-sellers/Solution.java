import java.util.*;

class Solution {
    public static String[] top_sellers(Map<String, Integer> sales, int k) {
        List<String> names = new ArrayList<>(sales.keySet());
        names.sort((a, b) -> {
            int byUnits = Integer.compare(sales.get(b), sales.get(a));
            return byUnits != 0 ? byUnits : a.compareTo(b);
        });
        return names.subList(0, Math.min(k, names.size())).toArray(new String[0]);
    }
}
