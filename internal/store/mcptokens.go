package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MCPToken is the stored record of one hal MCP capability token. Only the SHA-256 hash of the token is kept.
type MCPToken struct {
	TokenHash  string
	AgentRunID string
	// Kind names what the token is scoped to: "flow" (an agent node run) or "assist" (an assist session).
	Kind      string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// CreateMCPToken records a token hash. The row has no foreign key: the token is issued before the run's row exists.
func (s *Store) CreateMCPToken(ctx context.Context, t *MCPToken) error {
	if t.TokenHash == "" || t.AgentRunID == "" || t.Kind == "" {
		return fmt.Errorf("%w: token_hash, agent_run_id and kind required", ErrInvalid)
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = s.Now()
	}
	return s.write(ctx, func(tx *txn) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO mcp_tokens (token_hash, agent_run_id, kind, created_at) VALUES (?,?,?,?)`,
			t.TokenHash, t.AgentRunID, t.Kind, ts(t.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: insert mcp_token: %w", err)
		}
		return nil
	})
}

// RevokeMCPToken marks a token revoked. Revoking a revoked or unknown token is not an error.
func (s *Store) RevokeMCPToken(ctx context.Context, hash string) error {
	return s.write(ctx, func(tx *txn) error {
		_, err := tx.ExecContext(ctx, `UPDATE mcp_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, ts(s.now()), hash)
		return err
	})
}

// RevokeAllMCPTokens revokes every live token and returns how many it revoked. The daemon calls it at start: runs do
// not survive a restart, so neither may their tokens.
func (s *Store) RevokeAllMCPTokens(ctx context.Context) (int64, error) {
	var n int64
	err := s.write(ctx, func(tx *txn) error {
		res, err := tx.ExecContext(ctx, `UPDATE mcp_tokens SET revoked_at = ? WHERE revoked_at IS NULL`, ts(s.now()))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// GetMCPToken returns the record of a token hash (ErrNotFound when unknown).
func (s *Store) GetMCPToken(ctx context.Context, hash string) (*MCPToken, error) {
	var t MCPToken
	var created string
	var revoked sql.NullString
	err := s.rdb.QueryRowContext(ctx, `SELECT token_hash, agent_run_id, kind, created_at, revoked_at FROM mcp_tokens WHERE token_hash = ?`, hash).
		Scan(&t.TokenHash, &t.AgentRunID, &t.Kind, &created, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get mcp_token: %w", err)
	}
	if t.CreatedAt, err = parseTS(created); err != nil {
		return nil, err
	}
	t.RevokedAt, err = parseTSPtr(revoked)
	return &t, err
}
