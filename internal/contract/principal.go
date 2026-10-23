// Package contract defines shared interfaces and context models across modules.
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

// SessionInfo represents authenticated session state without raw tokens.
// PrivilegeVersion is the authorization revision captured by this session;
// CurrentPrivilegeVersion is read from users.privilege_version. A mismatch
// requires session-ID rotation before serving the protected request.
type SessionInfo struct {
	UserID                  int64
	CreatedAt               time.Time
	LastSeenAt              time.Time
	RotatedAt               time.Time
	AbsoluteExpiresAt       time.Time
	PrivilegeVersion        int64 // versi hak akses saat session terakhir dirotasi
	CurrentPrivilegeVersion int64 // versi hak akses user saat ini
	PrivilegeChanged        bool  // true jika hak akses berubah sejak session dibuat/dirotasi
	IsCurrentID             bool
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
	Data      any
}
