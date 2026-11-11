package db

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// MobileToken never serializes its verifier or the password-generation binding.
type MobileToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func mobilePasswordVersion(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:])
}

func (d *DB) CreateMobileToken(ctx context.Context, id, name string, digest []byte, passwordHash string, expires time.Time) (MobileToken, error) {
	var token MobileToken
	err := d.QueryRowContext(ctx, `INSERT INTO mobile_tokens(id,name,token_hash,password_version,expires_at) VALUES($1,$2,$3,$4,$5) RETURNING id,name,created_at,expires_at,last_used_at,revoked_at`, id, name, digest, mobilePasswordVersion(passwordHash), expires).Scan(&token.ID, &token.Name, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt)
	return token, err
}

// A password change invalidates all device tokens, even when individual revocation
// fails. Tokens expire after 90 days at most and after 30 days without use.
func (d *DB) ValidateMobileToken(ctx context.Context, digest []byte, passwordHash string) (*MobileToken, error) {
	var token MobileToken
	var version string
	err := d.QueryRowContext(ctx, `SELECT id,name,created_at,expires_at,last_used_at,revoked_at,password_version FROM mobile_tokens WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now() AND COALESCE(last_used_at,created_at)>now()-interval '30 days'`, digest).Scan(&token.ID, &token.Name, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(version), []byte(mobilePasswordVersion(passwordHash))) != 1 {
		return nil, nil
	}
	_, err = d.ExecContext(ctx, `UPDATE mobile_tokens SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at<now()-interval '1 minute')`, token.ID)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (d *DB) ListMobileTokens(ctx context.Context) ([]MobileToken, error) {
	rows, err := d.QueryContext(ctx, `SELECT id,name,created_at,expires_at,last_used_at,revoked_at FROM mobile_tokens ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MobileToken{}
	for rows.Next() {
		var t MobileToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) RevokeMobileToken(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE mobile_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id)
	return err
}
