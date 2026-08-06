package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

const defaultSoftClaimTTL = 5 * time.Minute

var softClaimNow = time.Now

// SoftClaim is an advisory, project-local intent to begin work on a Beads issue.
// It is deliberately separate from the authoritative Beads in-progress state.
type SoftClaim struct {
	Agent     string    `json:"agent"`
	BeadID    string    `json:"bead_id"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SoftClaimCheckResult is the stable response for claim inspection.
type SoftClaimCheckResult struct {
	Success bool       `json:"success"`
	BeadID  string     `json:"bead_id"`
	State   string     `json:"state"`
	Claim   *SoftClaim `json:"claim,omitempty"`
}

// SoftClaimListResult is the stable response for active claim listing.
type SoftClaimListResult struct {
	Success bool        `json:"success"`
	Claims  []SoftClaim `json:"claims"`
}

func newClaimCmd() *cobra.Command {
	var (
		agent string
		ttl   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "claim <bead-id>",
		Short: "Record an optional, expiring intent to work on a bead",
		Long: `Record an optional soft claim before changing a Beads issue to in_progress.

Soft claims reduce selection races in high-contention swarms. They are advisory:
the Beads tracker remains authoritative. A claim expires after five minutes by
default, and expired records are retained under .ntm/claims/expired for audit.

Examples:
  ntm claim ntm-g2lq --agent TopazMill
  ntm claim check ntm-g2lq
  ntm claim list`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			claim, err := createSoftClaim(GetProjectRoot(), args[0], agent, ttl)
			if err != nil {
				return err
			}
			return outputSoftClaim(cmd.OutOrStdout(), SoftClaimCheckResult{
				Success: true,
				BeadID:  claim.BeadID,
				State:   "claimed",
				Claim:   &claim,
			})
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Agent identity (defaults to AGENT_NAME or USER)")
	cmd.Flags().DurationVar(&ttl, "ttl", defaultSoftClaimTTL, "Claim lifetime")
	cmd.AddCommand(newClaimCheckCmd(), newClaimListCmd())
	return cmd
}

func newClaimCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <bead-id>",
		Short: "Check whether a bead has an active soft claim",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := checkSoftClaim(GetProjectRoot(), args[0])
			if err != nil {
				return err
			}
			return outputSoftClaim(cmd.OutOrStdout(), result)
		},
	}
}

func newClaimListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active soft claims in this project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			claims, err := listSoftClaims(GetProjectRoot())
			if err != nil {
				return err
			}
			return outputSoftClaimList(cmd.OutOrStdout(), SoftClaimListResult{Success: true, Claims: claims})
		},
	}
}

func outputSoftClaim(w io.Writer, result SoftClaimCheckResult) error {
	return output.OutputOrText(IsJSONOutput(), result, func() error {
		switch result.State {
		case "claimed":
			_, err := fmt.Fprintf(w, "claimed: %s by %s until %s\n", result.BeadID, result.Claim.Agent, result.Claim.ExpiresAt.UTC().Format(time.RFC3339))
			return err
		case "unclaimed":
			_, err := fmt.Fprintf(w, "unclaimed: %s\n", result.BeadID)
			return err
		case "expired":
			_, err := fmt.Fprintf(w, "expired: %s was claimed by %s until %s\n", result.BeadID, result.Claim.Agent, result.Claim.ExpiresAt.UTC().Format(time.RFC3339))
			return err
		default:
			return fmt.Errorf("unknown soft-claim state %q", result.State)
		}
	})
}

func outputSoftClaimList(w io.Writer, result SoftClaimListResult) error {
	return output.OutputOrText(IsJSONOutput(), result, func() error {
		if len(result.Claims) == 0 {
			_, err := fmt.Fprintln(w, "no active soft claims")
			return err
		}
		for _, claim := range result.Claims {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", claim.BeadID, claim.Agent, claim.ExpiresAt.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	})
}

func createSoftClaim(projectDir, beadID, agent string, ttl time.Duration) (SoftClaim, error) {
	beadID, err := validateSoftClaimBeadID(beadID)
	if err != nil {
		return SoftClaim{}, err
	}
	if ttl <= 0 {
		return SoftClaim{}, fmt.Errorf("claim TTL must be positive")
	}
	agent = softClaimAgent(agent)
	if agent == "" {
		return SoftClaim{}, fmt.Errorf("agent identity is required; pass --agent or set AGENT_NAME")
	}

	claimPath := softClaimPath(projectDir, beadID)
	if status, err := checkSoftClaim(projectDir, beadID); err != nil {
		return SoftClaim{}, err
	} else if status.State == "claimed" {
		return SoftClaim{}, fmt.Errorf("bead %s is already soft-claimed by %s until %s", beadID, status.Claim.Agent, status.Claim.ExpiresAt.UTC().Format(time.RFC3339))
	} else if status.State == "expired" {
		if err := archiveExpiredSoftClaim(projectDir, beadID, claimPath); err != nil {
			return SoftClaim{}, err
		}
	}

	if err := os.MkdirAll(filepath.Dir(claimPath), 0o755); err != nil {
		return SoftClaim{}, fmt.Errorf("create soft-claim directory: %w", err)
	}
	now := softClaimNow().UTC()
	claim := SoftClaim{Agent: agent, BeadID: beadID, ClaimedAt: now, ExpiresAt: now.Add(ttl)}
	if err := writeNewSoftClaim(claimPath, claim); err != nil {
		if errors.Is(err, os.ErrExist) {
			status, statusErr := checkSoftClaim(projectDir, beadID)
			if statusErr != nil {
				return SoftClaim{}, statusErr
			}
			if status.State == "claimed" {
				return SoftClaim{}, fmt.Errorf("bead %s is already soft-claimed by %s until %s", beadID, status.Claim.Agent, status.Claim.ExpiresAt.UTC().Format(time.RFC3339))
			}
		}
		return SoftClaim{}, err
	}
	return claim, nil
}

func writeNewSoftClaim(path string, claim SoftClaim) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create soft claim %s: %w", path, err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(claim); err != nil {
		_ = f.Close()
		return fmt.Errorf("write soft claim %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync soft claim %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close soft claim %s: %w", path, err)
	}
	return nil
}

func checkSoftClaim(projectDir, beadID string) (SoftClaimCheckResult, error) {
	beadID, err := validateSoftClaimBeadID(beadID)
	if err != nil {
		return SoftClaimCheckResult{}, err
	}
	claim, err := readSoftClaim(softClaimPath(projectDir, beadID))
	if errors.Is(err, os.ErrNotExist) {
		return SoftClaimCheckResult{Success: true, BeadID: beadID, State: "unclaimed"}, nil
	}
	if err != nil {
		return SoftClaimCheckResult{}, err
	}
	state := "claimed"
	if !softClaimNow().Before(claim.ExpiresAt) {
		state = "expired"
	}
	return SoftClaimCheckResult{Success: true, BeadID: beadID, State: state, Claim: &claim}, nil
}

func listSoftClaims(projectDir string) ([]SoftClaim, error) {
	dir := softClaimsDir(projectDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []SoftClaim{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read soft-claim directory: %w", err)
	}
	now := softClaimNow()
	claims := make([]SoftClaim, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		claim, err := readSoftClaim(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if now.Before(claim.ExpiresAt) {
			claims = append(claims, claim)
		}
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].BeadID < claims[j].BeadID })
	return claims, nil
}

func archiveExpiredSoftClaim(projectDir, beadID, claimPath string) error {
	claim, err := readSoftClaim(claimPath)
	if err != nil {
		return err
	}
	if softClaimNow().Before(claim.ExpiresAt) {
		return fmt.Errorf("bead %s is still soft-claimed by %s", beadID, claim.Agent)
	}
	expiredDir := filepath.Join(softClaimsDir(projectDir), "expired")
	if err := os.MkdirAll(expiredDir, 0o755); err != nil {
		return fmt.Errorf("create expired soft-claim directory: %w", err)
	}
	archivePath := filepath.Join(expiredDir, fmt.Sprintf("%s-%d.json", beadID, softClaimNow().UTC().UnixNano()))
	if _, err := os.Lstat(archivePath); err == nil {
		return fmt.Errorf("refusing to overwrite archived soft claim %s", archivePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect archived soft claim %s: %w", archivePath, err)
	}
	if err := os.Rename(claimPath, archivePath); err != nil {
		return fmt.Errorf("archive expired soft claim: %w", err)
	}
	return nil
}

func readSoftClaim(path string) (SoftClaim, error) {
	f, err := os.Open(path)
	if err != nil {
		return SoftClaim{}, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	var claim SoftClaim
	if err := decoder.Decode(&claim); err != nil {
		return SoftClaim{}, fmt.Errorf("decode soft claim %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return SoftClaim{}, fmt.Errorf("decode soft claim %s: multiple JSON values", path)
		}
		return SoftClaim{}, fmt.Errorf("decode soft claim %s: %w", path, err)
	}
	if _, err := validateSoftClaimBeadID(claim.BeadID); err != nil {
		return SoftClaim{}, fmt.Errorf("invalid soft claim %s: %w", path, err)
	}
	if strings.TrimSpace(claim.Agent) == "" || claim.ClaimedAt.IsZero() || claim.ExpiresAt.IsZero() {
		return SoftClaim{}, fmt.Errorf("invalid soft claim %s: required fields are missing", path)
	}
	return claim, nil
}

func softClaimsDir(projectDir string) string {
	return filepath.Join(projectDir, ".ntm", "claims")
}

func softClaimPath(projectDir, beadID string) string {
	return filepath.Join(softClaimsDir(projectDir), beadID+".json")
}

func softClaimAgent(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent != "" {
		return agent
	}
	for _, name := range []string{"AGENT_NAME", "NTM_AGENT_NAME", "USER"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func validateSoftClaimBeadID(beadID string) (string, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return "", fmt.Errorf("bead ID is required")
	}
	for _, r := range beadID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("invalid bead ID %q", beadID)
		}
	}
	return beadID, nil
}
