// Every syntax shape jvm_field.go propagates through, for TestTraceFieldGolden.
package cases;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

public class Cases {
    record Line(String sku, int qty) {}

    static class Holder {
        String id = "init";
        int[] slots = new int[2];
    }

    private String last = "none";

    static String clean(String raw) {
        return raw.trim();
    }

    String flow(String raw, int n) {
        String id = clean(raw);
        String quoted = ("[" + id + "]");
        Object boxed = (Object) quoted;
        String pick = n > 0 ? (String) boxed : id;
        int neg = -n;
        int sum = neg * 2 + n % 3;
        String[] arr = new String[] {pick};
        String first = arr[0];
        Holder h = new Holder();
        h.id = first;
        h.slots[0] = sum;
        this.last = h.id;
        Line line = new Line(first, sum);
        List<String> ids = new ArrayList<>();
        ids.add(line.sku());
        List<String> upper = ids.stream().map(s -> s.toUpperCase()).toList();
        String joined = "";
        for (String u : upper) {
            joined = joined + u;
        }
        String[] slots = new String[1];
        slots[0] = joined;
        UUID uuid = UUID.fromString(slots[0]);
        String out = uuid.toString() + last + Integer.toString(h.slots[0]) + arr.length;
        if (out.isBlank() || !out.equals(raw)) {
            return out;
        }
        return new Line(out, line.qty()).sku();
    }

    void caller() {
        System.out.println(flow(" 42 ", 3));
    }
}
