package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestRetryTransportRetriesIdempotentTransportFailures(t *testing.T) {
	var attempts atomic.Int64
	transport := &RetryTransport{MaxRetries: 2, Backoff: time.Millisecond, Base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("temporary connection failure")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header), Request: request}, nil
	})}
	request, _ := http.NewRequest(http.MethodGet, "http://upstream.test/health", nil)
	response, err := transport.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusOK || attempts.Load() != 3 {
		t.Fatalf("retry result response=%v attempts=%d err=%v", response, attempts.Load(), err)
	}
	_ = response.Body.Close()
}

func TestRetryTransportDoesNotReplayUnsafeMethodsOrHTTPResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		status int
	}{
		{name: "post is not retried", method: http.MethodPost},
		{name: "http error response is returned", method: http.MethodGet, status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int64
			transport := &RetryTransport{MaxRetries: 3, Base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				attempts.Add(1)
				if test.status != 0 {
					return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader("upstream error")), Header: make(http.Header), Request: request}, nil
				}
				return nil, errors.New("connection reset")
			})}
			request, _ := http.NewRequest(test.method, "http://upstream.test/resource", nil)
			response, err := transport.RoundTrip(request)
			if test.status != 0 {
				if err != nil || response.StatusCode != test.status || attempts.Load() != 1 {
					t.Fatalf("response retry result=%v attempts=%d err=%v", response, attempts.Load(), err)
				}
				_ = response.Body.Close()
			} else if err == nil || attempts.Load() != 1 {
				t.Fatalf("unsafe method should fail without retry: attempts=%d err=%v", attempts.Load(), err)
			}
		})
	}
}
