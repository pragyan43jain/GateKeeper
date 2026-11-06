package main

import (
	"net/http"
	"time"
)

// RetryTransport retries only body-replayable, idempotent requests after
// transport errors. HTTP responses, including 5xx responses, are returned as-is.
type RetryTransport struct {
	Base       http.RoundTripper
	MaxRetries int
	Backoff    time.Duration
}

func (t *RetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.MaxRetries <= 0 || !retryableMethod(request.Method) || !replayableBody(request) {
		return base.RoundTrip(request)
	}
	for attempt := 0; ; attempt++ {
		current := request
		if attempt > 0 {
			current = request.Clone(request.Context())
			if request.Body != nil && request.Body != http.NoBody {
				body, err := request.GetBody()
				if err != nil {
					return nil, err
				}
				current.Body = body
			}
		}
		response, err := base.RoundTrip(current)
		if err == nil || attempt >= t.MaxRetries || request.Context().Err() != nil {
			return response, err
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if t.Backoff > 0 {
			timer := time.NewTimer(t.Backoff)
			select {
			case <-request.Context().Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return nil, request.Context().Err()
			case <-timer.C:
			}
		}
	}
}

func retryableMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func replayableBody(request *http.Request) bool {
	return request.Body == nil || request.Body == http.NoBody || request.GetBody != nil
}

var _ http.RoundTripper = (*RetryTransport)(nil)
