package runner

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
)

func configuredJobs(
	repository repopath.Repository,
	branch string,
	commit string,
	config repoconfig.Config,
	created time.Time,
) ([]Job, error) {
	names := make([]string, 0, len(config.Jobs))
	for name, job := range config.Jobs {
		if job.MatchesBranch(branch) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	ids := make(map[string]string, len(names))
	for _, name := range names {
		id, err := newJobID()
		if err != nil {
			return nil, err
		}
		ids[name] = id
	}
	jobs := make([]Job, 0, len(names))
	for _, name := range names {
		jobConfig := config.Jobs[name]
		job := Job{
			ID:         ids[name],
			Name:       name,
			Repository: repository.Full(),
			Branch:     branch,
			Commit:     commit,
			Image:      jobConfig.Image,
			Status:     StatusQueued,
			CreatedAt:  created,
		}
		missingDependency := ""
		for _, need := range jobConfig.Needs {
			dependencyID, scheduled := ids[need]
			job.Needs = append(job.Needs, JobDependency{Name: need, ID: dependencyID})
			if !scheduled && missingDependency == "" {
				missingDependency = need
			}
		}
		if missingDependency != "" {
			finished := created
			job.Status = StatusFailed
			job.FinishedAt = &finished
			job.Error = fmt.Sprintf(
				"dependency %q does not run on branch %q",
				missingDependency,
				branch,
			)
		}
		if job.Status != StatusFailed {
			switch {
			case jobConfig.Manual:
				job.Status = StatusManual
			case len(job.Needs) > 0:
				job.Status = StatusWaiting
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func reconcileJobDependencies(jobs []Job, now time.Time) (updates, promoted []Job) {
	byID := make(map[string]*Job, len(jobs))
	for index := range jobs {
		byID[jobs[index].ID] = &jobs[index]
	}
	changed := make(map[string]struct{})
	for madeProgress := true; madeProgress; {
		madeProgress = false
		for index := range jobs {
			job := &jobs[index]
			if len(job.Needs) == 0 ||
				(job.Status != StatusWaiting && job.Status != StatusManual) {
				continue
			}
			ready, dependencyErr := dependencyReadiness(*job, byID)
			if dependencyErr != nil {
				job.Status = StatusFailed
				job.FinishedAt = timePointer(now)
				job.Error = dependencyErr.Error()
				changed[job.ID] = struct{}{}
				madeProgress = true
				continue
			}
			if ready && job.Status == StatusWaiting {
				job.Status = StatusQueued
				changed[job.ID] = struct{}{}
				promoted = append(promoted, *job)
				madeProgress = true
			}
		}
	}
	for _, job := range jobs {
		if _, found := changed[job.ID]; found {
			updates = append(updates, job)
		}
	}
	return updates, promoted
}

func jobDependenciesReady(job Job, jobs []Job) (bool, error) {
	byID := make(map[string]*Job, len(jobs))
	for index := range jobs {
		byID[jobs[index].ID] = &jobs[index]
	}
	return dependencyReadiness(job, byID)
}

func successfulDependencies(
	names []string,
	job Job,
	jobs []Job,
) ([]JobDependency, error) {
	dependencies := make([]JobDependency, 0, len(names))
	for _, name := range names {
		found := false
		for _, candidate := range jobs {
			if candidate.Name == name && candidate.Branch == job.Branch &&
				candidate.Commit == job.Commit && candidate.Status == StatusSucceeded {
				dependencies = append(dependencies, JobDependency{Name: name, ID: candidate.ID})
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("dependency %q has no successful run", name)
		}
	}
	return dependencies, nil
}

func dependencyReadiness(job Job, jobs map[string]*Job) (bool, error) {
	ready := true
	for _, need := range job.Needs {
		dependency, found := jobs[need.ID]
		if !found {
			return false, fmt.Errorf("dependency %q is missing", need.Name)
		}
		switch dependency.Status {
		case StatusSucceeded:
		case StatusFailed:
			return false, fmt.Errorf("dependency %q failed", need.Name)
		case StatusCanceled:
			return false, fmt.Errorf("dependency %q was canceled", need.Name)
		default:
			ready = false
		}
	}
	return ready, nil
}

func dependencyNames(job Job) string {
	if len(job.Needs) == 0 {
		return "none"
	}
	names := make([]string, 0, len(job.Needs))
	for _, need := range job.Needs {
		names = append(names, need.Name)
	}
	return strings.Join(names, ", ")
}

func timePointer(value time.Time) *time.Time {
	return &value
}
