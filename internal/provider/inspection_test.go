package provider

import (
	"context"
	"errors"
	"testing"
)

func TestContentInspectionRecognizesVendorCodes(t *testing.T) {
	cases := []error{
		&APIError{Kind: KindBadRequest, Status: 400, Code: "InternalError.Algo.DataInspectionFailed", Text: "Input text data may contain inappropriate content."},
		&APIError{Kind: KindBadRequest, Status: 400, Code: "DataInspectionFailed", Detail: "内容审核未通过"},
		&APIError{Kind: KindServer, Status: 400, Type: "server_error", Code: "data_inspection_failed"},
		&APIError{Kind: KindBadRequest, Code: "content_filter"},
		&APIError{Kind: KindOther, Text: "ResponseDataInspectionFailed"},
	}
	for _, err := range cases {
		if !ContentInspection(err) {
			t.Fatalf("want inspection: %+v", err)
		}
		if Retryable(err) {
			t.Fatalf("inspection must not be retried: %+v", err)
		}
	}
}

func TestContentInspectionRejectsNonReviewErrors(t *testing.T) {
	cases := []error{
		context.Canceled,
		context.DeadlineExceeded,
		&APIError{Kind: KindTransport, Text: "connection reset"},
		&APIError{Kind: KindTimeout, Text: "stream idle timeout"},
		&APIError{Kind: KindRateLimit, Status: 429, Code: "Throttling"},
		&APIError{Kind: KindContextOverflow, Text: "Range of input length"},
		&APIError{Kind: KindAuth, Code: "Arrearage"},
		&APIError{Kind: KindServer, Type: "server_error", Text: "internal error"},
		&APIError{Kind: KindBadRequest, Status: 400, Text: "invalid argument: tools"},
		errors.New("EOF"),
	}
	for _, err := range cases {
		if ContentInspection(err) {
			t.Fatalf("must not classify as inspection: %+v", err)
		}
	}
}
