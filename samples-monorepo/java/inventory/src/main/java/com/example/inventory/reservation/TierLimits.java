package com.example.inventory.reservation;

/** How many units of one SKU a customer of each tier may reserve per order. */
public final class TierLimits {
    private TierLimits() {}

    public static int perSku(String tier) {
        if ("gold".equals(tier)) {
            return Integer.MAX_VALUE;
        } else if ("silver".equals(tier)) {
            return 50;
        }
        return 20;
    }
}
