package app

import (
	"context"
	"errors"
	"sort"
	"strings"
)

var ErrDiscoveryUnavailable = errors.New("local Web service discovery is unavailable")

// DiscoveryCandidate is a transient, read-only observation. It is deliberately
// not an AppSpec and is never persisted by discovery.
type DiscoveryCandidate struct {
	Key                    string `json:"key"`
	DisplayName            string `json:"displayName"`
	Description            string `json:"description,omitempty"`
	Protocol               string `json:"protocol"`
	Port                   uint16 `json:"port"`
	Path                   string `json:"path"`
	IconURI                string `json:"iconUri,omitempty"`
	Source                 string `json:"source"`
	SourceDetail           string `json:"sourceDetail,omitempty"`
	Address                string `json:"address,omitempty"`
	GroupKey               string `json:"groupKey,omitempty"`
	ContainerID            string `json:"containerId,omitempty"`
	NetworkMode            string `json:"networkMode,omitempty"`
	OwnerConfidence        string `json:"ownerConfidence,omitempty"`
	PID                    int    `json:"pid,omitempty"`
	Preferred              bool   `json:"preferred"`
	WatchCow               bool   `json:"watchCow"`
	ExistingApplication    string `json:"existingApplication,omitempty"`
	RegistrationSuggestion string `json:"registrationSuggestion"`
}

// Discoverer is the seam between the application workflow and read-only host
// observation. Its implementation must not create, change, or remove a target.
type Discoverer interface {
	Discover(context.Context) ([]DiscoveryCandidate, error)
}

func (s *Service) Discover(ctx context.Context) ([]DiscoveryCandidate, error) {
	if s.Discoverer == nil {
		return nil, ErrDiscoveryUnavailable
	}
	candidates, err := s.Discoverer.Discover(ctx)
	if err != nil {
		return nil, err
	}
	specs, err := s.Repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		candidate := &candidates[index]
		candidate.Protocol = strings.ToLower(strings.TrimSpace(candidate.Protocol))
		if candidate.Protocol != "https" {
			candidate.Protocol = "http"
		}
		if candidate.Path == "" {
			candidate.Path = "/"
		}
		candidate.RegistrationSuggestion = "available"
		if candidate.ExistingApplication != "" {
			candidate.RegistrationSuggestion = "existing-fnos-application"
		}
		for _, spec := range specs {
			if spec.Protocol == candidate.Protocol && spec.Port == candidate.Port && spec.Path == candidate.Path {
				candidate.RegistrationSuggestion = "already-registered"
				candidate.ExistingApplication = spec.AppName
				break
			}
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].RegistrationSuggestion != candidates[right].RegistrationSuggestion {
			return candidates[left].RegistrationSuggestion < candidates[right].RegistrationSuggestion
		}
		if candidates[left].DisplayName != candidates[right].DisplayName {
			return strings.ToLower(candidates[left].DisplayName) < strings.ToLower(candidates[right].DisplayName)
		}
		return candidates[left].Port < candidates[right].Port
	})
	return candidates, nil
}
