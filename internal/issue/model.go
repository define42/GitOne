package issue

import "time"

type State string

const (
	StateOpen   State = "open"
	StateClosed State = "closed"
)

// The maximum constants bound a persisted issue so that a single record cannot
// grow without limit. The title and body limits match the merge request limits
// enforced by the review store.
const (
	MaximumTitleBytes    = 500
	MaximumBodyBytes     = 64 * 1024
	MaximumBranchBytes   = 1024
	MaximumLabels        = 32
	MaximumLabelBytes    = 100
	MaximumAssignees     = 32
	MaximumAssigneeBytes = 320
)

// Issue is one persisted repository issue. Issues are numbered per repository
// and independently of merge requests: issues are referenced as `#<id>` and
// merge requests as `!<id>`.
type Issue struct {
	ID              uint64     `json:"id"`
	Repository      string     `json:"repository"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Author          string     `json:"author"`
	State           State      `json:"state"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	Labels          []string   `json:"labels"`
	Assignees       []string   `json:"assignees"`
	Comments        []Comment  `json:"comments"`
	Branch          string     `json:"branch,omitempty"`
	BranchCreatedBy string     `json:"branchCreatedBy,omitempty"`
	BranchCreatedAt *time.Time `json:"branchCreatedAt,omitempty"`
	ClosedBy        string     `json:"closedBy,omitempty"`
	ClosedAt        *time.Time `json:"closedAt,omitempty"`
}

// Comment is one entry in an issue's discussion.
type Comment struct {
	ID        uint64    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}
