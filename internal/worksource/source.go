package worksource

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	StaleCode       = "STALE_WORK_COORDINATION"
	NoClaimableCode = "NO_CLAIMABLE_WORK"
	maxRecordBytes  = 8 << 20
)

// ErrStale shares the existing source-capture sentinel; all consumers can use
// errors.Is without depending on how a mismatch was discovered.
var ErrStale = ErrChanged

// SameSource compares a bound receipt using the canonical identity contract.
func (i Identity) SameSource(other Identity) bool {
	return i.Bound() && i.Verify(other) == nil
}

// StaleError retains both identities when available for a remediation receipt.
type StaleError struct {
	Expected *Identity `json:"expected,omitempty"`
	Observed *Identity `json:"observed,omitempty"`
	Reason   string    `json:"reason"`
	Cause    error     `json:"-"`
}

func (e *StaleError) Error() string { return StaleCode + ": " + e.Reason }
func (e *StaleError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrStale}
	}
	return []error{ErrStale, e.Cause}
}

// Policy permits dirty/detached development checkouts by default. RequiredRef
// resolves an already-local, full refs/ name; it never fetches or checks out.
type Policy struct {
	Expected     *Identity
	RequiredRef  string
	RequireClean bool
}

// Snapshot keeps authorization data private. Dirty is advisory metadata, not
// part of Identity equality; ordinary edits must not invalidate work receipts.
type Snapshot struct {
	Identity Identity
	Dirty    bool
	issues   map[string]issue
}

type issue struct {
	ID           string       `json:"id"`
	Status       string       `json:"status"`
	Type         string       `json:"issue_type"`
	Assignee     string       `json:"assignee"`
	Labels       []string     `json:"labels"`
	Dependencies []dependency `json:"dependencies"`
	DeferUntil   string       `json:"defer_until"`
	Pinned       bool         `json:"pinned"`
	Ephemeral    bool         `json:"ephemeral"`
	IsTemplate   bool         `json:"is_template"`
}

type dependency struct {
	ID   string `json:"depends_on_id"`
	Type string `json:"type"`
}

// Read parses the exact bytes named by Capture, retaining the existing source
// identity implementation rather than introducing another Git/digest engine.
// Call Validate after collecting tool results as well. This is evidence
// validation, not a replacement for the final atomic tracker claim.
func Read(ctx context.Context, project string, policy Policy) (*Snapshot, error) {
	if ctx == nil {
		return nil, errors.New("work source requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(project) == "" {
		return nil, stale(policy.Expected, nil, "explicit project directory is required", nil)
	}
	identity, err := Capture(ctx, project)
	if err != nil {
		return nil, stale(policy.Expected, nil, "capture canonical Beads source", err)
	}
	if !identity.Bound() {
		return nil, stale(policy.Expected, &identity, "canonical Beads JSONL is unavailable", nil)
	}
	if policy.Expected != nil && !policy.Expected.SameSource(identity) {
		return nil, stale(policy.Expected, &identity, "cached work source no longer matches; recollect work before dispatch", nil)
	}
	file, err := os.Open(identity.JSONLPath)
	if err != nil {
		return nil, stale(policy.Expected, &identity, "open canonical Beads JSONL", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: file}, maxTrackerBytes+1))
	readErr = errors.Join(readErr, file.Close())
	if readErr != nil || int64(len(data)) > maxTrackerBytes {
		return nil, stale(policy.Expected, &identity, "read canonical Beads JSONL within the 64 MiB limit", readErr)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != identity.JSONLSHA256 {
		return nil, stale(&identity, nil, "tracker changed before eligibility records were read", nil)
	}
	current, err := Capture(ctx, project)
	if err != nil || identity.Verify(current) != nil {
		return nil, stale(&identity, &current, "source changed while reading eligibility records", err)
	}
	if policy.RequiredRef != "" {
		ref := strings.TrimSpace(policy.RequiredRef)
		if !strings.HasPrefix(ref, "refs/") || strings.ContainsAny(ref, " ~^:?*[\\\x00\r\n") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
			return nil, stale(policy.Expected, &identity, "required ref must be a full literal refs/ name", nil)
		}
		out, refErr := gitOutput(ctx, identity.ProjectDir, "rev-parse", "--verify", ref+"^{commit}")
		if refErr != nil || out != identity.HeadSHA {
			return nil, stale(policy.Expected, &identity, "checkout does not match required local ref", refErr)
		}
	}
	dirty := false
	if identity.HeadSHA != "" {
		// --no-optional-locks is already enforced by gitOutput. Disable a
		// configured fsmonitor hook too: verification must remain read-only.
		out, statusErr := gitOutput(ctx, identity.ProjectDir, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "--untracked-files=normal", "--", ".")
		if statusErr != nil {
			return nil, stale(policy.Expected, &identity, "inspect checkout dirtiness", statusErr)
		}
		dirty = out != ""
	}
	if policy.RequireClean && (identity.HeadSHA == "" || strings.HasPrefix(identity.HeadSHA, "unborn:") || dirty) {
		return nil, stale(policy.Expected, &identity, "policy requires a clean committed checkout", nil)
	}
	issues, err := decodeIssues(ctx, data)
	if err != nil {
		return nil, stale(policy.Expected, &identity, "invalid canonical Beads JSONL", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Snapshot{Identity: identity, Dirty: dirty, issues: issues}, nil
}

// Validate never repairs mismatches or replaces an expected identity silently.
func Validate(ctx context.Context, expected Identity) error {
	_, err := Read(ctx, expected.ProjectDir, Policy{Expected: &expected})
	return err
}

func stale(expected, observed *Identity, reason string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	if cause != nil {
		reason += ": " + cause.Error()
	}
	return &StaleError{Expected: expected, Observed: observed, Reason: reason, Cause: cause}
}

func decodeIssues(ctx context.Context, data []byte) (map[string]issue, error) {
	rows := make(map[string]issue)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), maxRecordBytes)
	for line := 1; scanner.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text := bytes.TrimSpace(scanner.Bytes())
		if len(text) == 0 {
			continue
		}
		var row issue
		if err := json.Unmarshal(text, &row); err != nil {
			return nil, fmt.Errorf("malformed record at line %d", line)
		}
		row.ID, row.Status = strings.TrimSpace(row.ID), normalized(row.Status)
		if row.ID == "" || row.Status == "" {
			return nil, fmt.Errorf("missing issue identity or lifecycle at line %d", line)
		}
		if _, exists := rows[row.ID]; exists {
			return nil, fmt.Errorf("duplicate issue identity at line %d", line)
		}
		rows[row.ID] = row
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read JSONL record: %w", err)
	}
	return rows, nil
}

func normalized(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
