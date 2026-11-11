package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestMobileTokenLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(seed[:])
	id := hex.EncodeToString(seed[:16])
	defer d.Exec(`DELETE FROM mobile_tokens WHERE id=$1`, id)
	token, err := d.CreateMobileToken(ctx, id, "iPhone 17", digest[:], "password-v1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if token.ID != id {
		t.Fatal("unexpected id")
	}
	valid, err := d.ValidateMobileToken(ctx, digest[:], "password-v1")
	if err != nil || valid == nil {
		t.Fatalf("valid token failed: %v", err)
	}
	wrong, err := d.ValidateMobileToken(ctx, digest[:], "password-v2")
	if err != nil || wrong != nil {
		t.Fatal("password change did not invalidate token")
	}
	if _, err = d.Exec(`UPDATE mobile_tokens SET last_used_at=now()-interval '31 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	idle, err := d.ValidateMobileToken(ctx, digest[:], "password-v1")
	if err != nil || idle != nil {
		t.Fatal("idle token accepted")
	}
	if _, err = d.Exec(`UPDATE mobile_tokens SET last_used_at=now(), expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	expired, err := d.ValidateMobileToken(ctx, digest[:], "password-v1")
	if err != nil || expired != nil {
		t.Fatal("expired token accepted")
	}
	if _, err = d.Exec(`UPDATE mobile_tokens SET expires_at=now()+interval '1 day' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = d.RevokeMobileToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	revoked, err := d.ValidateMobileToken(ctx, digest[:], "password-v1")
	if err != nil || revoked != nil {
		t.Fatal("revoked token accepted")
	}
}
