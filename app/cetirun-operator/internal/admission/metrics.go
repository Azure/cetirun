// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	admissionDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cetirun_sandbox_admission_decisions_total",
		Help: "Sandbox admission decisions by handler and bounded outcome, including denials returned over HTTP 200.",
	}, []string{"handler", "outcome"})
	servingCertificateExpiry = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cetirun_sandbox_admission_certificate_not_after_seconds",
		Help: "Unix expiry time of the current serving certificate; zero when serving material cannot be read or parsed.",
	})
)

func init() {
	metrics.Registry.MustRegister(admissionDecisions, servingCertificateExpiry)
}

func instrumentAdmission(name string, handler admission.HandlerFunc) admission.HandlerFunc {
	return func(ctx context.Context, request admission.Request) admission.Response {
		response := handler(ctx, request)
		outcome := "denied"
		switch {
		case response.Allowed:
			outcome = "allowed"
		case response.Result != nil && response.Result.Code >= http.StatusInternalServerError:
			outcome = "dependency_error"
		case response.Result != nil && response.Result.Code == http.StatusBadRequest:
			outcome = "invalid_request"
		}
		admissionDecisions.WithLabelValues(name, outcome).Inc()
		return response
	}
}
