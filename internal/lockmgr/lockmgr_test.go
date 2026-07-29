package lockmgr

import (
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func TestUnrelatedRepositoriesDoNotBlock(t *testing.T) {
	manager := New()
	first := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	second := repopath.Repository{Groups: []string{"engineering"}, Name: "web"}
	releaseFirst, err := manager.Acquire(RepositoryRequests("/srv/gitone", []repopath.Repository{first}, Exclusive)...)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	acquired := make(chan func(), 1)
	go func() {
		release, acquireErr := manager.Acquire(
			RepositoryRequests("/srv/gitone", []repopath.Repository{second}, Exclusive)...,
		)
		if acquireErr != nil {
			acquired <- nil
			return
		}
		acquired <- release
	}()
	select {
	case release := <-acquired:
		if release == nil {
			t.Fatal("could not acquire unrelated repository lock")
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("unrelated repository lock was blocked")
	}
}

func TestSameRepositoryBlocks(t *testing.T) {
	manager := New()
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	requests := RepositoryRequests("/srv/gitone", []repopath.Repository{repository}, Exclusive)
	releaseFirst, err := manager.Acquire(requests...)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, _ := manager.Acquire(requests...)
		acquired <- release
	}()
	select {
	case release := <-acquired:
		release()
		t.Fatal("same repository lock did not block")
	case <-time.After(50 * time.Millisecond):
	}
	releaseFirst()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("same repository lock did not resume")
	}
}

func TestGroupWriteBlocksDescendantRepository(t *testing.T) {
	manager := New()
	releaseGroup, err := manager.Acquire(
		GroupRequests("/srv/gitone", []string{"engineering"}, Exclusive)...,
	)
	if err != nil {
		t.Fatal(err)
	}

	repository := repopath.Repository{
		Groups: []string{"engineering", "backend"},
		Name:   "api",
	}
	acquired := make(chan func(), 1)
	go func() {
		release, _ := manager.Acquire(
			RepositoryRequests("/srv/gitone", []repopath.Repository{repository}, Exclusive)...,
		)
		acquired <- release
	}()
	select {
	case release := <-acquired:
		release()
		t.Fatal("descendant repository bypassed group lock")
	case <-time.After(50 * time.Millisecond):
	}
	releaseGroup()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("descendant repository did not resume")
	}
}

func TestCatalogWriteWaitsForRepositoryReaders(t *testing.T) {
	manager := New()
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	releaseRepository, err := manager.Acquire(
		RepositoryRequests("/srv/gitone", []repopath.Repository{repository}, Shared)...,
	)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, _ := manager.Acquire(CatalogRequest("/srv/gitone", Exclusive))
		acquired <- release
	}()
	select {
	case release := <-acquired:
		release()
		t.Fatal("catalog write bypassed repository reader")
	case <-time.After(50 * time.Millisecond):
	}
	releaseRepository()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("catalog write did not resume")
	}
}

func TestLFSLocksAreScopedToAGroup(t *testing.T) {
	manager := New()
	releaseEngineering, err := manager.Acquire(
		LFSRequests("/srv/gitone", "engineering")...,
	)
	if err != nil {
		t.Fatal(err)
	}

	releaseFinance, err := manager.Acquire(LFSRequests("/srv/gitone", "finance")...)
	if err != nil {
		t.Fatal("unrelated group LFS lock was blocked:", err)
	}
	releaseFinance()

	acquired := make(chan func(), 1)
	go func() {
		release, _ := manager.Acquire(LFSRequests("/srv/gitone", "engineering")...)
		acquired <- release
	}()
	select {
	case release := <-acquired:
		release()
		t.Fatal("same-group LFS lock did not block")
	case <-time.After(50 * time.Millisecond):
	}
	releaseEngineering()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("same-group LFS lock did not resume")
	}
}

func TestJobLocksAllowDifferentJobs(t *testing.T) {
	manager := New()
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	releaseFirst, err := manager.Acquire(JobRequest("/srv/gitone", repository, "first"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()

	releaseSecond, err := manager.Acquire(JobRequest("/srv/gitone", repository, "second"))
	if err != nil {
		t.Fatal("different job lock was blocked:", err)
	}
	releaseSecond()
}

func TestMultipleRequestsAreOrderedAndDeduplicated(t *testing.T) {
	manager := New()
	first := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	second := repopath.Repository{Groups: []string{"engineering"}, Name: "web"}
	requests := append(
		RepositoryRequests("/srv/gitone", []repopath.Repository{second}, Exclusive),
		RepositoryRequests("/srv/gitone", []repopath.Repository{first}, Exclusive)...,
	)
	requests = append(requests, RepositoryRequests(
		"/srv/gitone",
		[]repopath.Repository{first},
		Shared,
	)...)
	release, err := manager.Acquire(requests...)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()

	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if len(manager.locks) != 0 {
		t.Fatalf("unused lock entries were retained: %d", len(manager.locks))
	}
}
