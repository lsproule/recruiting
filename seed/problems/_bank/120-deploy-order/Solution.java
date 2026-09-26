import java.util.*;

class Solution {
    public static String[] deploy_order(String[] services, String[][] deps) {
        Map<String, Integer> indegree = new HashMap<>();
        Map<String, List<String>> after = new HashMap<>();
        for (String s : services) {
            indegree.put(s, 0);
            after.put(s, new ArrayList<>());
        }
        for (String[] d : deps) {
            after.get(d[0]).add(d[1]);
            indegree.merge(d[1], 1, Integer::sum);
        }
        PriorityQueue<String> ready = new PriorityQueue<>();
        for (String s : services) if (indegree.get(s) == 0) ready.add(s);
        List<String> order = new ArrayList<>();
        while (!ready.isEmpty()) {
            String s = ready.poll();
            order.add(s);
            for (String t : after.get(s)) {
                if (indegree.merge(t, -1, Integer::sum) == 0) ready.add(t);
            }
        }
        return order.size() == services.length ? order.toArray(new String[0]) : new String[0];
    }
}
