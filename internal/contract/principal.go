// Package contract berisi kontrak minimum yang dipakai lintas modul.
// File ini adalah draft scaffold. Jika tim sudah membekukan kontrak di repository,
// pertahankan versi tim dan sesuaikan implementasi P1 terhadapnya.
package contract

import (
	"context"
	"time"
)

type Principal struct {
	UserID          int64
	Username        string
	IsPlatformAdmin bool
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

type SessionRevoker interface {
	RevokeAllForUser(ctx context.Context, userID int64) error
}

// SessionInfo sengaja tidak memuat token session mentah.
type SessionInfo struct {
	UserID            int64
	CreatedAt         time.Time
	LastSeenAt        time.Time
	RotatedAt         time.Time
	AbsoluteExpiresAt time.Time
	IsCurrentID       bool
}

type pageDataContextKey struct{}

func WithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, pageDataContextKey{}, token)
}

func CSRFTokenFrom(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(pageDataContextKey{}).(string)
	return token, ok
}

type PageData struct {
	Principal Principal
	CSRFToken string
	Flash     string
}
