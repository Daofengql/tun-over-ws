package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const claimsKey contextKey = "jwt_claims"

// withClaims stores JWT claims in context.
func withClaims(ctx context.Context, claims *JWTClaims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// GetClaims retrieves JWT claims from context.
func GetClaims(ctx context.Context) *JWTClaims {
	claims, _ := ctx.Value(claimsKey).(*JWTClaims)
	return claims
}

// HashPassword hashes a password using bcrypt.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// checkPassword compares a password with a bcrypt hash.
func checkPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// hashToken creates a SHA256 hash of a token for storage.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
