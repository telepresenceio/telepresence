package usg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// errInfo is the privacy-safe summary of a failed command's error. It carries
// only tool-intrinsic identifiers — the errcat category, the chain of Go error
// types, and any gRPC or HTTP status code — never message text.
type errInfo struct {
	category  string // errcat category name, e.g. "user"
	chain     string // unwrap chain, outer->inner, joined by ">"
	grpcCode  string // gRPC code name, e.g. "Unavailable"; empty if none
	httpCode  string // numeric HTTP status, e.g. "404"; empty if none
	k8sReason string // k8s status reason, e.g. "NotFound"; empty if none
}

// analyzeError decomposes err into its privacy-safe parts. The category and
// chain are always populated for a non-nil err; the code fields are populated
// only when a corresponding status is found anywhere in the chain.
func analyzeError(err error) errInfo {
	ei := errInfo{
		category: errcat.GetCategory(err).String(),
		chain:    errChain(err),
		grpcCode: grpcCode(err),
	}
	var apiStatus apierrors.APIStatus
	if errors.As(err, &apiStatus) {
		s := apiStatus.Status()
		if s.Code != 0 {
			ei.httpCode = strconv.Itoa(int(s.Code))
		}
		ei.k8sReason = string(s.Reason)
	}
	return ei
}

// addTo records the populated fields on r as separate entries. Empty fields are
// omitted. Calls on a nil Report are no-ops (Add handles that).
func (ei errInfo) addTo(r *Report) {
	r.Add("error.cat", ei.category)
	r.Add("error.chain", ei.chain)
	if ei.grpcCode != "" {
		r.Add("error.grpc", ei.grpcCode)
	}
	if ei.httpCode != "" {
		r.Add("error.http", ei.httpCode)
	}
	if ei.k8sReason != "" {
		r.Add("error.k8s", ei.k8sReason)
	}
}

// errChain renders the unwrap chain from outermost to innermost as a
// ">"-separated list of error identities. Structural wrappers that carry no
// diagnostic signal (fmt's wrapError, errcat's categorized) are dropped, well-
// known sentinels are rendered by name, and consecutive duplicates are
// collapsed. Branches of a multi-error (Unwrap() []error) are joined with "|".
func errChain(err error) string {
	tokens := chainTokens(err, nil)
	out := make([]byte, 0, 64)
	for i, tok := range tokens {
		if i > 0 {
			out = append(out, '>')
		}
		out = append(out, tok...)
	}
	return string(out)
}

func chainTokens(err error, acc []string) []string {
	for err != nil {
		if tok := errToken(err); tok != "" {
			// Collapse consecutive duplicates (e.g. repeated wrappers that
			// survived the skip list).
			if n := len(acc); n == 0 || acc[n-1] != tok {
				acc = append(acc, tok)
			}
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			branches := x.Unwrap()
			if len(branches) == 0 {
				return acc
			}
			// Render branches as alternatives. The first continues the current
			// line; the rest are bracketed and "|"-joined onto the last token.
			for i, b := range branches {
				if i == 0 {
					acc = chainTokens(b, acc)
				} else {
					sub := errChain(b)
					if sub != "" && len(acc) > 0 {
						acc[len(acc)-1] += "|" + sub
					}
				}
			}
			return acc
		default:
			return acc
		}
	}
	return acc
}

// errToken returns the chain token for a single error frame, or "" if the frame
// is a pure structural wrapper that should be skipped.
//
// Sentinels are matched by identity (not errors.Is) so that a typed error which
// merely *wraps* a sentinel — e.g. a *net.OpError around context.DeadlineExceeded
// — reports its own type rather than collapsing to the sentinel name.
func errToken(err error) string {
	switch err { //nolint:errorlint // identity match is intentional; see doc comment
	case context.Canceled:
		return "context.Canceled"
	case context.DeadlineExceeded:
		return "context.DeadlineExceeded"
	case io.EOF:
		return "io.EOF"
	case os.ErrNotExist:
		return "os.ErrNotExist"
	}
	switch t := fmt.Sprintf("%T", err); t {
	case "*fmt.wrapError", "*fmt.wrapErrors", "*errors.joinError", "*errcat.categorized":
		return ""
	default:
		return t
	}
}

// grpcCode returns the gRPC status code name if err carries one. It checks both
// the standard status interface (server-side and direct calls) and the
// telepresence StructuredError shape, which exposes GetCode() but neither
// GRPCStatus() nor Unwrap() and is therefore invisible to status.FromError.
func grpcCode(err error) string {
	if s, ok := status.FromError(err); ok {
		if c := s.Code(); c != codes.OK && c != codes.Unknown {
			return c.String()
		}
	}
	var coder interface{ GetCode() codes.Code }
	if errors.As(err, &coder) {
		if c := coder.GetCode(); c != codes.OK && c != codes.Unknown {
			return c.String()
		}
	}
	return ""
}
