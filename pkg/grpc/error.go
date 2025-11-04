package grpc

import (
	"encoding/json"
	"errors"

	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Error interface {
	Error() string
	GRPCStatus() *status.Status
}

type StructuredError struct {
	Code     grpcCodes.Code  `json:"code"`
	Message  string          `json:"message"`
	Category errcat.Category `json:"category"`
}

type structuredGrpcError struct {
	StructuredError
	status *status.Status
}

func (e *StructuredError) GetCode() grpcCodes.Code {
	return e.Code
}

func (e *structuredGrpcError) GRPCStatus() *status.Status {
	return e.status
}

func (e *StructuredError) Error() string {
	return e.Message
}

func (e *StructuredError) GetCategory() errcat.Category {
	return e.Category
}

func FromGRPC(err error) error {
	if err != nil {
		var grpcErr Error
		if errors.As(err, &grpcErr) {
			st := grpcErr.GRPCStatus()
			var es StructuredError
			if json.Unmarshal([]byte(st.Message()), &es) == nil {
				return &es
			} else {
				return &structuredGrpcError{
					StructuredError: StructuredError{
						Code:     st.Code(),
						Message:  st.Message(),
						Category: errcat.Unknown,
					},
					status: st,
				}
			}
		}
	}
	return err
}
