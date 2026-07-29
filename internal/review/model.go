package review

import "time"

type State string

const (
	StateOpen   State = "open"
	StateClosed State = "closed"
	StateMerged State = "merged"
)

type MergeRequest struct {
	ID                  uint64     `json:"id"`
	Repository          string     `json:"repository"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	Target              string     `json:"target"`
	Source              string     `json:"source"`
	Author              string     `json:"author"`
	State               State      `json:"state"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	BaseCommit          string     `json:"baseCommit"`
	HeadCommit          string     `json:"headCommit"`
	RequiredApprovals   int        `json:"requiredApprovals"`
	Approvals           []Approval `json:"approvals"`
	Threads             []Thread   `json:"threads"`
	MergedCommit        string     `json:"mergedCommit,omitempty"`
	MergedStrategy      string     `json:"mergedStrategy,omitempty"`
	MergedBy            string     `json:"mergedBy,omitempty"`
	MergedAt            *time.Time `json:"mergedAt,omitempty"`
	ClosedBy            string     `json:"closedBy,omitempty"`
	ClosedAt            *time.Time `json:"closedAt,omitempty"`
	MergeInProgress     bool       `json:"mergeInProgress,omitempty"`
	MergeClaimID        string     `json:"mergeClaimId,omitempty"`
	MergeOwnerID        string     `json:"mergeOwnerId,omitempty"`
	MergeHeadCommit     string     `json:"mergeHeadCommit,omitempty"`
	MergeTargetCommit   string     `json:"mergeTargetCommit,omitempty"`
	MergeResultCommit   string     `json:"mergeResultCommit,omitempty"`
	MergeResultStrategy string     `json:"mergeResultStrategy,omitempty"`
	MergeStartedBy      string     `json:"mergeStartedBy,omitempty"`
	MergeStartedAt      *time.Time `json:"mergeStartedAt,omitempty"`
}

type Approval struct {
	Author     string    `json:"author"`
	HeadCommit string    `json:"headCommit"`
	CreatedAt  time.Time `json:"createdAt"`
	// SelfApproval retains the historical JSON field name for stored review compatibility.
	SelfApproval bool `json:"ownerOverride,omitempty"`
}

type Thread struct {
	ID         uint64     `json:"id"`
	CreatedAt  time.Time  `json:"createdAt"`
	Resolved   bool       `json:"resolved"`
	ResolvedBy string     `json:"resolvedBy,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	Comments   []Comment  `json:"comments"`
}

type Comment struct {
	ID        uint64    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}
