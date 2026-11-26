package com.example.payments.risk;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayDeque;
import java.util.Deque;
import java.util.HashMap;
import java.util.Map;
import org.springframework.stereotype.Component;

/** Declines a customer's fourth approved charge within one minute. */
@Component
public class VelocityCheck {
    static final int MAX_PER_WINDOW = 3;
    static final Duration WINDOW = Duration.ofMinutes(1);

    private final Clock clock;
    private final Map<String, Deque<Instant>> recent = new HashMap<>();

    public VelocityCheck(Clock clock) {
        this.clock = clock;
    }

    /** @return true and counts the charge, or false when the customer is over the limit */
    public synchronized boolean admit(String customerId) {
        Instant now = clock.instant();
        Deque<Instant> times = recent.computeIfAbsent(customerId, k -> new ArrayDeque<>());
        while (!times.isEmpty() && !times.peekFirst().isAfter(now.minus(WINDOW))) {
            times.pollFirst();
        }
        if (times.size() >= MAX_PER_WINDOW) {
            return false;
        }
        times.addLast(now);
        return true;
    }
}
