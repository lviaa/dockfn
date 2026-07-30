package app

import (
	"context"
	"testing"
)

type discoveryFixture struct {
	items []DiscoveryCandidate
	err   error
}

func (f discoveryFixture) Discover(context.Context) ([]DiscoveryCandidate, error) {
	return append([]DiscoveryCandidate(nil), f.items...), f.err
}

func TestDiscoverMarksExistingRegistrationsWithoutPersistingCandidates(t *testing.T) {
	service, repository, _ := testService(t)
	repository.apps["012345abcdef"] = AppSpec{
		ID: "012345abcdef", AppName: "existing.dkfn", DisplayName: "Existing",
		Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
	service.Discoverer = discoveryFixture{items: []DiscoveryCandidate{
		{Key: "docker:demo:8080", DisplayName: "Demo", Protocol: "HTTP", Port: 8080, Path: "/", Source: "docker"},
		{Key: "docker:watchcow:3000", DisplayName: "WatchCow", Protocol: "https", Port: 3000, Path: "/", Source: "docker", ExistingApplication: "watchcow.demo"},
	}}
	items, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].RegistrationSuggestion != "already-registered" || items[0].ExistingApplication != "existing.dkfn" {
		t.Fatalf("DockFN registration was not identified: %#v", items)
	}
	if items[1].RegistrationSuggestion != "existing-fnos-application" {
		t.Fatalf("fnOS match was not preserved: %#v", items[1])
	}
	if got, _ := repository.List(context.Background()); len(got) != 1 {
		t.Fatalf("discovery persisted a candidate: %#v", got)
	}
}

func TestDiscoverRequiresConfiguredDiscoverer(t *testing.T) {
	service, _, _ := testService(t)
	if _, err := service.Discover(context.Background()); err != ErrDiscoveryUnavailable {
		t.Fatalf("err=%v", err)
	}
}
