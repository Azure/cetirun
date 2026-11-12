// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// ChartDownloadError reports that a chart could not be downloaded from its
// registry. Registries redirect downloads to their storage, and the cause of
// a failed download quotes the storage URL; the message keeps the cause
// without the query strings of the URLs in it.
type ChartDownloadError struct {
	// Chart is the chart reference, repository and version.
	Chart string
	// Version is the chart version.
	Version string
	// Err is the cause, as the registry client returned it.
	Err error
}

func (e *ChartDownloadError) Error() string {
	return fmt.Sprintf("locate OCI chart %s: %s", e.Chart, withoutURLQueries(e.Err.Error()))
}

func (e *ChartDownloadError) Unwrap() error { return e.Err }

// ChartDownloadClass is the kind of a chart download failure. It comes from
// the typed errors of the registry client, not from the message text.
type ChartDownloadClass string

const (
	// ChartDownloadUnauthorized: the registry refused the credential (HTTP 401 or 403).
	ChartDownloadUnauthorized ChartDownloadClass = "unauthorized"
	// ChartDownloadNotFound: the registry has no such chart or version.
	ChartDownloadNotFound ChartDownloadClass = "notFound"
	// ChartDownloadNetwork: the registry could not be reached.
	ChartDownloadNetwork ChartDownloadClass = "network"
	// ChartDownloadUnknown: any other failure.
	ChartDownloadUnknown ChartDownloadClass = "unknown"
)

// Class returns the kind of the download failure.
func (e *ChartDownloadError) Class() ChartDownloadClass {
	if e == nil || e.Err == nil {
		return ChartDownloadUnknown
	}
	var response *errcode.ErrorResponse
	if errors.As(e.Err, &response) {
		switch {
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			return ChartDownloadUnauthorized
		case response.StatusCode == http.StatusNotFound:
			return ChartDownloadNotFound
		case response.StatusCode >= 500:
			return ChartDownloadNetwork
		}
		return ChartDownloadUnknown
	}
	if errors.Is(e.Err, errdef.ErrNotFound) {
		return ChartDownloadNotFound
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	var urlErr *url.Error
	var opErr *net.OpError
	if errors.As(e.Err, &dnsErr) || errors.As(e.Err, &opErr) || errors.As(e.Err, &urlErr) ||
		errors.As(e.Err, &netErr) || errors.Is(e.Err, context.DeadlineExceeded) {
		return ChartDownloadNetwork
	}
	return ChartDownloadUnknown
}

// urlQuery matches an HTTP or HTTPS URL that has a query string or fragment,
// up to the next space or quote.
var urlQuery = regexp.MustCompile(`(https?://[^\s"'?#]*)[?#][^\s"']*`)

// withoutURLQueries removes the query strings and fragments of the URLs in s.
func withoutURLQueries(s string) string {
	return urlQuery.ReplaceAllString(s, "$1")
}
