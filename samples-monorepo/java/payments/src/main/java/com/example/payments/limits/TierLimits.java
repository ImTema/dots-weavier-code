package com.example.payments.limits;

import java.util.OptionalLong;

/** The most one charge may take, by customer tier. */
public final class TierLimits {
    private TierLimits() {}

    /** @return the limit in cents, empty when the tier has none; unknown tiers are standard */
    public static OptionalLong limitCents(String tier) {
        return switch (tier == null ? "" : tier) {
            case "gold" -> OptionalLong.empty();
            case "silver" -> OptionalLong.of(200_000);
            default -> OptionalLong.of(50_000);
        };
    }
}
