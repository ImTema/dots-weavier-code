// Package httpapi is the HTTP transport for orders.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"example.com/samples/orders/api"
	"example.com/samples/orders/internal/customer"
	"example.com/samples/orders/internal/notify"
	"example.com/samples/orders/internal/order"
)

const maxBody = 1 << 20

// Handler serves the orders HTTP API.
type Handler struct {
	orders    *order.Service
	customers *customer.Store
	notifier  notify.Notifier
}

// New returns a Handler.
func New(orders *order.Service, customers *customer.Store, n notify.Notifier) *Handler {
	return &Handler{orders: orders, customers: customers, notifier: n}
}

// Routes returns the HTTP routes.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", h.placeOrder)
	mux.HandleFunc("GET "+api.CustomerPath+"{id}", h.getCustomer)
	return mux
}

func (h *Handler) placeOrder(w http.ResponseWriter, r *http.Request) {
	var req api.PlaceOrderRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		http.Error(w, "malformed JSON", http.StatusBadRequest)
		return
	}
	ctx := notify.WithNotifier(r.Context(), h.notifier)
	o, err := h.orders.Place(ctx, req)
	if errors.Is(err, order.ErrInvalid) || errors.Is(err, customer.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if err != nil {
		slog.ErrorContext(ctx, "place order", "err", err)
		http.Error(w, "pricing unavailable", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, api.PlaceOrderResponse{OrderID: o.ID, TotalCents: o.TotalCents})
}

// getCustomer serves the HTTP hop pricing makes back into orders.
func (h *Handler) getCustomer(w http.ResponseWriter, r *http.Request) {
	c, err := h.customers.Lookup(r.PathValue("id"))
	if errors.Is(err, customer.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}
