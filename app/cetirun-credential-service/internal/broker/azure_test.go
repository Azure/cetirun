// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestARMClientDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	client := newAzureClient(time.Second)
	client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://attacker.example.invalid/collect"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    &http.Request{URL: &url.URL{Scheme: "https", Host: armHost, Path: "/subscriptions"}},
		}, nil
	})
	if _, err := client.subscriptions(context.Background(), "Bearer synthetic"); err == nil {
		t.Fatal("ARM redirect was accepted")
	}
	if requests != 1 {
		t.Fatalf("ARM redirect caused %d requests, want 1", requests)
	}
}

func TestARMListFollowsSafeContinuationLinks(t *testing.T) {
	requests := 0
	client := &azureClient{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Scheme != "https" || request.URL.Host != armHost || request.URL.Path != "/subscriptions" {
			t.Fatalf("unexpected ARM request URL: %s", request.URL)
		}
		body := `{"value":[{"subscriptionId":"one","displayName":"One","state":"Enabled"}],"nextLink":"https://management.azure.com/subscriptions?api-version=2022-12-01&$skiptoken=opaque"}`
		if requests == 2 {
			body = `{"value":[{"subscriptionId":"two","displayName":"Two","state":"Enabled"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := client.subscriptions(context.Background(), "Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || result.Truncated || len(result.Items) != 2 {
		t.Fatalf("requests=%d truncated=%t items=%d; want 2, false, 2", requests, result.Truncated, len(result.Items))
	}
}

func TestARMListRejectsUnsafeContinuationLinks(t *testing.T) {
	client := &azureClient{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"value":[],"nextLink":"https://evil.example.invalid/subscriptions"}`))}, nil
	})}}
	if _, err := client.subscriptions(context.Background(), "Bearer synthetic"); err == nil {
		t.Fatal("unsafe ARM continuation link was accepted")
	}
	for _, link := range []string{
		"http://management.azure.com/subscriptions",
		"https://user@management.azure.com/subscriptions",
		"https://management.azure.com/subscriptions#fragment",
		"https://management.azure.com/providers/Microsoft.ResourceGraph/resources",
	} {
		if _, err := validARMNextLink(link, "/subscriptions"); err == nil {
			t.Fatalf("unsafe continuation link was accepted: %s", link)
		}
	}
}

func TestARMListTruncatesAtOneHundredItems(t *testing.T) {
	values := make([]map[string]string, maxResultItems+1)
	for index := range values {
		values[index] = map[string]string{"subscriptionId": "subscription", "displayName": "name", "state": "Enabled"}
	}
	payload, err := json.Marshal(map[string]any{"value": values})
	if err != nil {
		t.Fatal(err)
	}
	client := &azureClient{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}}
	result, err := client.subscriptions(context.Background(), "Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Items) != maxResultItems {
		t.Fatalf("truncated=%t items=%d; want true, %d", result.Truncated, len(result.Items), maxResultItems)
	}
}

func TestARMListStopsAtTwentyPagesWithContinuation(t *testing.T) {
	requests := 0
	client := &azureClient{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests > maxARMPages {
			t.Fatalf("requested page %d after the page limit", requests)
		}
		if request.URL.Path != "/subscriptions" {
			t.Fatalf("unexpected ARM request URL: %s", request.URL)
		}
		body := `{"value":[{"subscriptionId":"subscription","displayName":"name","state":"Enabled"}],"nextLink":"https://management.azure.com/subscriptions?api-version=2022-12-01&$skiptoken=opaque"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := client.subscriptions(context.Background(), "Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if requests != maxARMPages || !result.Truncated || len(result.Items) != maxARMPages {
		t.Fatalf("requests=%d truncated=%t items=%d; want %d, true, %d", requests, result.Truncated, len(result.Items), maxARMPages, maxARMPages)
	}
}

func TestResourceGroupListRejectsContinuationOutsideCollection(t *testing.T) {
	const subscription = "11111111-1111-1111-1111-111111111111"
	client := &azureClient{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"value":[],"nextLink":"https://management.azure.com/subscriptions/11111111-1111-1111-1111-111111111111/resourcegroups/unexpected?api-version=2021-04-01"}`))}, nil
	})}}
	if _, err := client.resourceGroups(context.Background(), "Bearer synthetic", subscription); err == nil {
		t.Fatal("resource group continuation outside its collection was accepted")
	}
}

func TestResourceGraphResultMarksOnlySearchAsTruncated(t *testing.T) {
	data := make([]map[string]string, maxResultItems+1)
	for index := range data {
		data[index] = map[string]string{"id": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/example", "name": "example"}
	}
	payload, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	client := &azureClient{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body["query"].(string), "limit 101") || body["options"].(map[string]any)["$top"] != float64(maxResultItems+1) {
			t.Fatalf("Resource Graph query was not limited to 101: %#v", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}}
	filters := resourceFilters{Subscriptions: []string{"11111111-1111-1111-1111-111111111111"}}
	search, err := client.resources(context.Background(), "Bearer synthetic", filters, "")
	if err != nil || !search.Truncated || len(search.Items) != maxResultItems {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	show, err := client.resources(context.Background(), "Bearer synthetic", filters, "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/example")
	if err != nil || show.Truncated || len(show.Items) != maxResultItems {
		t.Fatalf("show=%#v err=%v", show, err)
	}
}
