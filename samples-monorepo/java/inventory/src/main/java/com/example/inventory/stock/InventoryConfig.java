package com.example.inventory.stock;

import io.smallrye.config.ConfigMapping;
import java.util.Map;

/** Seed stock, read from {@code inventory.stock.<sku>=<qty>}. */
@ConfigMapping(prefix = "inventory")
public interface InventoryConfig {
    Map<String, Integer> stock();
}
