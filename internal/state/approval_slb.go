package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ApprovalSLBLink binds one local approval to its external request and the
// canonical directory in which SLB created that request. It contains no
// session key and never replaces the approval's operation correlation key.
type ApprovalSLBLink struct {
	ApprovalID string
	RequestID  string
	ProjectDir string
}

// CreateApprovalSLBLink records immutable external identity. Repeating the
// exact write is safe; neither another request nor another local approval may
// take over an existing link. The optional display-only correlation annotation
// and the link are committed together without changing a landed decision.
func (s *Store) CreateApprovalSLBLink(ctx context.Context, link ApprovalSLBLink) error {
	if strings.TrimSpace(link.ApprovalID) == "" || strings.TrimSpace(link.RequestID) == "" ||
		strings.TrimSpace(link.RequestID) != link.RequestID || !filepath.IsAbs(link.ProjectDir) {
		return errors.New("SLB link requires an approval, exact request ID and absolute project directory")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SLB link: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO approval_slb_links (approval_id, request_id, project_dir)
		SELECT id, ?, ? FROM approvals WHERE id = ? AND requires_slb = 1
		ON CONFLICT(approval_id) DO NOTHING`, link.RequestID, link.ProjectDir, link.ApprovalID)
	if err != nil {
		return fmt.Errorf("create SLB link: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read SLB link insertion: %w", err)
	}
	if rows == 0 {
		var current ApprovalSLBLink
		err := tx.QueryRowContext(ctx, `SELECT approval_id, request_id, project_dir
			FROM approval_slb_links WHERE approval_id = ?`, link.ApprovalID).
			Scan(&current.ApprovalID, &current.RequestID, &current.ProjectDir)
		if err != nil || current != link {
			return fmt.Errorf("SLB link unavailable or already bound for approval %q", link.ApprovalID)
		}
	}
	// Preserve the familiar annotation only for uncorrelated requests. The
	// separate link remains authoritative, even when an operation uses slb:
	// as part of its own correlation string.
	if _, err := tx.ExecContext(ctx, `UPDATE approvals SET correlation_id = ?
		WHERE id = ? AND (correlation_id IS NULL OR correlation_id = '')`,
		"slb:"+link.RequestID, link.ApprovalID); err != nil {
		return fmt.Errorf("annotate SLB link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SLB link: %w", err)
	}
	return nil
}

// GetApprovalSLBLink returns nil for native-only or historical approvals whose
// external project was never recorded. A correlation string is not evidence
// that a request was actually mirrored, and must not be used as a substitute.
func (s *Store) GetApprovalSLBLink(ctx context.Context, approvalID string) (*ApprovalSLBLink, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var link ApprovalSLBLink
	err := s.db.QueryRowContext(ctx, `SELECT approval_id, request_id, project_dir
		FROM approval_slb_links WHERE approval_id = ?`, approvalID).
		Scan(&link.ApprovalID, &link.RequestID, &link.ProjectDir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get SLB link: %w", err)
	}
	if strings.TrimSpace(link.RequestID) == "" || strings.TrimSpace(link.RequestID) != link.RequestID || !filepath.IsAbs(link.ProjectDir) {
		return nil, fmt.Errorf("invalid stored SLB link for approval %q", approvalID)
	}
	return &link, nil
}
