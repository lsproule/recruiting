import java.util.*;

class Solution {
    public static Map<String, Integer> word_tally(String[] words) {
        Map<String, Integer> counts = new HashMap<>();
        for (String w : words) counts.merge(w, 1, Integer::sum);
        return counts;
    }
}
