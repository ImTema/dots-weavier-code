package com.example.inventory.stock;

import jakarta.enterprise.inject.Produces;
import jakarta.inject.Singleton;

final class StockLedgerProducer {
    @Produces
    @Singleton
    StockLedger stockLedger(InventoryConfig config) {
        return new StockLedger(config.stock());
    }
}
