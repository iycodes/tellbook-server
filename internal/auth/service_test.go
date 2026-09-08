package auth

import (
	"testing"
	"time"

	"booking/go-server/internal/config"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestAccessTokenSecurityRevisionIsRequired(t *testing.T) {
	service := NewService(nil, config.Config{
		AuthAccessTokenSecret: "test-secret",
		AuthAccessTokenTTL:    time.Hour,
		AuthIssuer:            "tellbook-test",
	}, nil, nil)
	user := User{ID: uuid.New(), FullName: "Provider", SecurityRevision: 3}
	token, err := service.signAccessToken(user, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	claims := &AccessTokenClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(token, claims)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if claims.SecurityRevision != user.SecurityRevision {
		t.Fatalf("security revision = %d, want %d", claims.SecurityRevision, user.SecurityRevision)
	}
}
