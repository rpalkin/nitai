package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	secret := "test-secret-key-12345678901234"
	userID := "user-123"
	orgID := "org-456"

	token, err := SignToken(userID, orgID, secret)
	if err != nil {
		t.Fatalf("SignToken failed: %v", err)
	}

	claims, err := VerifyToken(token, secret)
	if err != nil {
		t.Fatalf("VerifyToken failed: %v", err)
	}

	if claims.UserID != userID {
		t.Errorf("UserID mismatch: got %s, want %s", claims.UserID, userID)
	}

	if claims.OrgID != orgID {
		t.Errorf("OrgID mismatch: got %s, want %s", claims.OrgID, orgID)
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	secret := "test-secret-key-12345678901234"

	// Create an expired token
	claims := Claims{
		UserID: "user-123",
		OrgID:  "org-456",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("Failed to sign token: %v", err)
	}

	_, err = VerifyToken(tokenStr, secret)
	if err == nil {
		t.Error("Expected error for expired token, got nil")
	}
}

func TestVerifyTokenInvalidSecret(t *testing.T) {
	secret := "test-secret-key-12345678901234"
	wrongSecret := "wrong-secret-key-1234567890123"
	userID := "user-123"
	orgID := "org-456"

	token, err := SignToken(userID, orgID, secret)
	if err != nil {
		t.Fatalf("SignToken failed: %v", err)
	}

	_, err = VerifyToken(token, wrongSecret)
	if err == nil {
		t.Error("Expected error for invalid secret, got nil")
	}
}
