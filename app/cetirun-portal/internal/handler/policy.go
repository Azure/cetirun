// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// authorizeTeamOwner resolves a Team membership against only signed Entra
// claims.  Absence of a groups claim (including Entra group overage) is a
// deliberate deny: the Portal never calls a graph endpoint as an implicit
// authorization fallback.
func (h *InstanceHandler) authorizeTeamOwner(ctx context.Context, teamName string, claims auth.Claims) (*cetirunv1alpha1.Team, cetirunv1alpha1.EntraSubject, error) {
	if claims.TenantID == "" || claims.ObjectID == "" {
		return nil, cetirunv1alpha1.EntraSubject{}, fmt.Errorf("a verified Entra identity is required")
	}
	team, err := h.Kube.GetTeam(ctx, teamName)
	if err != nil {
		return nil, cetirunv1alpha1.EntraSubject{}, err
	}
	for _, subject := range team.Spec.Membership.Subjects {
		if !strings.EqualFold(subject.TenantID, claims.TenantID) {
			continue
		}
		switch subject.Kind {
		case "User":
			if strings.EqualFold(subject.ObjectID, claims.ObjectID) {
				return team, subject, nil
			}
		case "Group":
			for _, group := range claims.Groups {
				if strings.EqualFold(subject.ObjectID, group) {
					return team, subject, nil
				}
			}
		}
	}
	return nil, cetirunv1alpha1.EntraSubject{}, fmt.Errorf("the verified owner is not a member of Team %q", teamName)
}

func newTeamAuthorization(subject cetirunv1alpha1.EntraSubject, now time.Time) *cetirunv1alpha1.TeamAuthorization {
	return &cetirunv1alpha1.TeamAuthorization{Subject: subject, AuthorizedAt: metav1.NewTime(now.UTC())}
}
