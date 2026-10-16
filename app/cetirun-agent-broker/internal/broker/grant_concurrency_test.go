// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGrantCreateUsesOneActiveRecordPerInstanceConnection(t *testing.T) {
	fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
	// An old random-ID record must not prevent recreation after its revocation.
	fixture.mutateGrant(t, func(grant *AgentGrant) {
		grant.Revoked = true
		grant.Revision++
	})

	base := fixture.server.store.client.Transport
	var storageMu sync.Mutex
	listed := make(chan struct{}, 2)
	grantListsRead := make(chan struct{}, 2)
	releaseListReads := make(chan struct{})
	releaseCreate := make(chan struct{})
	fixture.server.store.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		isGrantList := request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/credentials/secrets" && strings.Contains(request.URL.RawQuery, "agenttools.cetirun.microsoft.com%2Finstance-uid")
		if isGrantList {
			listed <- struct{}{}
			<-releaseListReads
		}
		storageMu.Lock()
		response, err := base.RoundTrip(request)
		storageMu.Unlock()
		if isGrantList {
			grantListsRead <- struct{}{}
			<-releaseCreate
		}
		return response, err
	})

	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { responses <- grantCreateRequest(t, fixture) }()
	}
	<-listed
	<-listed
	close(releaseListReads)
	<-grantListsRead
	<-grantListsRead
	close(releaseCreate)
	first, second := <-responses, <-responses
	fixture.server.store.client.Transport = base

	statuses := map[int]int{first.Code: 1, second.Code: 1}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent create statuses = %d, %d; want one 200 and one 409", first.Code, second.Code)
	}
	grants, err := fixture.server.store.listGrants(context.Background(), fixture.grant.Owner, fixture.grant.InstanceUID)
	if err != nil {
		t.Fatal(err)
	}
	active := []AgentGrant{}
	for _, grant := range grants {
		if grant.ConnectionID == fixture.connection.Connection.ID && activeGrant(grant, time.Now()) {
			active = append(active, grant)
		}
	}
	if len(active) != 1 || active[0].ID != grantKey(fixture.grant.InstanceUID, fixture.connection.Connection.ID) {
		t.Fatalf("active grants after concurrent create = %#v", active)
	}

	// A newly deterministic record can also be revoked and recreated; its
	// resource-version update must not leave two active grants behind.
	record, version, err := fixture.server.store.getGrant(context.Background(), active[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Grant.Revoked = true
	record.Grant.Revision++
	if err := fixture.server.store.putGrant(context.Background(), record, version); err != nil {
		t.Fatal(err)
	}
	response := grantCreateRequest(t, fixture)
	if response.Code != http.StatusOK {
		t.Fatalf("recreate after revoke = %d: %s", response.Code, response.Body.String())
	}
	var recreated AgentGrant
	if err := json.NewDecoder(response.Result().Body).Decode(&recreated); err != nil {
		t.Fatal(err)
	}
	if recreated.ID != active[0].ID || recreated.Revision <= record.Grant.Revision {
		t.Fatalf("recreated grant = %#v; revoked record = %#v", recreated, record.Grant)
	}
	grants, err = fixture.server.store.listGrants(context.Background(), fixture.grant.Owner, fixture.grant.InstanceUID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, grant := range grants {
		if grant.ConnectionID == fixture.connection.Connection.ID && activeGrant(grant, time.Now()) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("active grants after revoke/recreate = %d, want 1", count)
	}
}

func grantCreateRequest(t *testing.T, fixture *authorizationFixture) *httptest.ResponseRecorder {
	t.Helper()
	input := grantRequest{
		ConnectionID: fixture.connection.Connection.ID,
		Tools:        fixture.grant.Tools,
		Constraints:  fixture.grant.Constraints,
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/instances/agent/tool-grants", bytes.NewReader(body))
	request.SetPathValue("name", fixture.agent.Metadata.Name)
	request.Header.Set("X-CetiRun-Owner-Tenant-ID", fixture.grant.Owner.TenantID)
	request.Header.Set("X-CetiRun-Owner-Object-ID", fixture.grant.Owner.ObjectID)
	request.Header.Set("X-CetiRun-Instance-UID", fixture.grant.InstanceUID)
	response := httptest.NewRecorder()
	fixture.server.grantCreate(response, request)
	return response
}
