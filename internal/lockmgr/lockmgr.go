package lockmgr

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/define42/GitOne/internal/repopath"
)

type Mode uint8

const (
	Shared Mode = iota
	Exclusive
)

type Request struct {
	Key  string
	Mode Mode
}

type entry struct {
	mutex sync.RWMutex
	refs  int
}

type Manager struct {
	mutex sync.Mutex
	locks map[string]*entry
}

func New() *Manager {
	return &Manager{locks: make(map[string]*entry)}
}

// Process coordinates every storage mutation in the single GitOne process.
//
//nolint:gochecknoglobals // GitOne intentionally has one process-wide lock manager.
var Process = New()

func (m *Manager) Acquire(requests ...Request) (func(), error) {
	normalized, err := normalizeRequests(requests)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return func() {}, nil
	}

	entries := make([]*entry, len(normalized))
	m.mutex.Lock()
	for index, request := range normalized {
		candidate := m.locks[request.Key]
		if candidate == nil {
			candidate = &entry{}
			m.locks[request.Key] = candidate
		}
		candidate.refs++
		entries[index] = candidate
	}
	m.mutex.Unlock()

	for index, request := range normalized {
		if request.Mode == Exclusive {
			entries[index].mutex.Lock()
		} else {
			entries[index].mutex.RLock()
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			for index := len(normalized) - 1; index >= 0; index-- {
				if normalized[index].Mode == Exclusive {
					entries[index].mutex.Unlock()
				} else {
					entries[index].mutex.RUnlock()
				}
			}
			m.mutex.Lock()
			for index, request := range normalized {
				entries[index].refs--
				if entries[index].refs == 0 {
					delete(m.locks, request.Key)
				}
			}
			m.mutex.Unlock()
		})
	}, nil
}

func normalizeRequests(requests []Request) ([]Request, error) {
	byKey := make(map[string]Mode, len(requests))
	for _, request := range requests {
		if request.Key == "" {
			return nil, fmt.Errorf("lock key is required")
		}
		if request.Mode != Shared && request.Mode != Exclusive {
			return nil, fmt.Errorf("invalid lock mode for %q", request.Key)
		}
		if existing, found := byKey[request.Key]; !found || request.Mode > existing {
			byKey[request.Key] = request.Mode
		}
	}
	normalized := make([]Request, 0, len(byKey))
	for key, mode := range byKey {
		normalized = append(normalized, Request{Key: key, Mode: mode})
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].Key < normalized[right].Key
	})
	return normalized, nil
}

func RepositoryRequests(
	root string,
	repositories []repopath.Repository,
	mode Mode,
) []Request {
	requests := []Request{CatalogRequest(root, Shared)}
	for _, repository := range repositories {
		requests = append(requests, groupAncestryRequests(root, repository.Group(), Shared)...)
		requests = append(requests, Request{
			Key:  resourceKey("20-repository", root, repository.Full()),
			Mode: mode,
		})
	}
	return requests
}

func GroupRequests(root string, groups []string, mode Mode) []Request {
	requests := []Request{CatalogRequest(root, Shared)}
	for _, group := range groups {
		requests = append(requests, groupAncestryRequests(root, group, mode)...)
	}
	return requests
}

func LFSRequests(root, group string) []Request {
	requests := []Request{CatalogRequest(root, Shared)}
	requests = append(requests, groupAncestryRequests(root, group, Shared)...)
	return append(requests, Request{
		Key:  resourceKey("30-lfs", root, group),
		Mode: Exclusive,
	})
}

func QueueRequest(root string) Request {
	return Request{Key: resourceKey("40-runner-queue", root, "queue"), Mode: Exclusive}
}

func CatalogRequest(root string, mode Mode) Request {
	return Request{Key: resourceKey("00-catalog", root, "catalog"), Mode: mode}
}

func JobRequest(root string, repository repopath.Repository, id string) Request {
	return Request{
		Key:  resourceKey("41-runner-job", root, repository.Full()+"/"+id),
		Mode: Exclusive,
	}
}

func ReviewCatalogRequest(root string, mode Mode) Request {
	return Request{Key: resourceKey("49-review-catalog", root, "reviews"), Mode: mode}
}

func ReviewRepositoryRequests(
	root string,
	repositories []repopath.Repository,
) []Request {
	requests := []Request{ReviewCatalogRequest(root, Shared)}
	for _, repository := range repositories {
		requests = append(
			requests,
			reviewGroupAncestryRequests(root, repository.Group(), Shared)...,
		)
		requests = append(requests, Request{
			Key:  resourceKey("51-review-repository", root, repository.Full()),
			Mode: Exclusive,
		})
	}
	return requests
}

func ReviewGroupRequests(root string, groups []string) []Request {
	requests := []Request{ReviewCatalogRequest(root, Shared)}
	for _, group := range groups {
		requests = append(requests, reviewGroupAncestryRequests(root, group, Exclusive)...)
	}
	return requests
}

func MergeRequest(root string, repository repopath.Repository) Request {
	return Request{
		Key:  resourceKey("60-merge", root, repository.Full()),
		Mode: Exclusive,
	}
}

func groupAncestryRequests(root, group string, terminal Mode) []Request {
	parts := strings.Split(group, "/")
	requests := make([]Request, 0, len(parts))
	for index := range parts {
		mode := Shared
		if index == len(parts)-1 {
			mode = terminal
		}
		requests = append(requests, Request{
			Key:  resourceKey("10-group", root, strings.Join(parts[:index+1], "/")),
			Mode: mode,
		})
	}
	return requests
}

func reviewGroupAncestryRequests(root, group string, terminal Mode) []Request {
	parts := strings.Split(group, "/")
	requests := make([]Request, 0, len(parts))
	for index := range parts {
		mode := Shared
		if index == len(parts)-1 {
			mode = terminal
		}
		requests = append(requests, Request{
			Key: resourceKey(
				"50-review-group",
				root,
				strings.Join(parts[:index+1], "/"),
			),
			Mode: mode,
		})
	}
	return requests
}

func resourceKey(kind, root, value string) string {
	canonical, err := filepath.Abs(root)
	if err != nil {
		canonical = filepath.Clean(root)
	} else if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	scope := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%s:%x:%s", kind, scope, value)
}
