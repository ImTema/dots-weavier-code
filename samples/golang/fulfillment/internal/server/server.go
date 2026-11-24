// Package server is the gRPC transport for fulfillment.
package server

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fulfillmentv1 "example.com/samples/fulfillment/api/fulfillment/v1"
	"example.com/samples/fulfillment/internal/plan"
)

// Server implements fulfillmentv1.FulfillmentServiceServer.
type Server struct {
	fulfillmentv1.UnimplementedFulfillmentServiceServer
	planner *plan.Planner
}

// New returns a Server planning shipments with planner.
func New(planner *plan.Planner) *Server { return &Server{planner: planner} }

// Schedule is the gRPC handler for fulfillment.v1.FulfillmentService/Schedule.
func (s *Server) Schedule(ctx context.Context, req *fulfillmentv1.ScheduleRequest) (*fulfillmentv1.ScheduleResponse, error) {
	sh, dup, err := s.planner.Plan(ctx, plan.Order{
		ID:         req.GetOrderId(),
		CustomerID: req.GetCustomerId(),
		Units:      req.GetUnits(),
		TotalCents: req.GetTotalCents(),
	})
	switch {
	case errors.Is(err, plan.ErrInvalid):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, plan.ErrUnsupportedRegion):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case err != nil:
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &fulfillmentv1.ScheduleResponse{
		ShipmentId:     sh.ID,
		Warehouse:      sh.Warehouse,
		Carrier:        sh.Carrier,
		ShippingCents:  sh.ShippingCents,
		InsuranceCents: sh.InsuranceCents,
		Duplicate:      dup,
	}, nil
}
