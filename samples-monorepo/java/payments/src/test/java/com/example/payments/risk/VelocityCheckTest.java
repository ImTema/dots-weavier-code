package com.example.payments.risk;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import java.time.ZoneOffset;
import org.junit.jupiter.api.Test;

class VelocityCheckTest {
    /** A clock the test moves by hand. */
    static final class StepClock extends Clock {
        Instant now = Instant.parse("2026-01-01T00:00:00Z");

        @Override public ZoneId getZone() { return ZoneOffset.UTC; }
        @Override public Clock withZone(ZoneId zone) { return this; }
        @Override public Instant instant() { return now; }
    }

    @Test
    void fourthChargeInAMinuteIsDeclined() {
        var clock = new StepClock();
        var v = new VelocityCheck(clock);
        assertTrue(v.admit("c1"));
        assertTrue(v.admit("c1"));
        assertTrue(v.admit("c1"));
        assertFalse(v.admit("c1"), "fourth within the window");
        assertTrue(v.admit("c2"), "other customers have their own window");

        clock.now = clock.now.plus(Duration.ofSeconds(59));
        assertFalse(v.admit("c1"), "still within the window");
        clock.now = clock.now.plus(Duration.ofSeconds(1));
        assertTrue(v.admit("c1"), "the first three left the window");
    }
}
