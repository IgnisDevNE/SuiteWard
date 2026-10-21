package appauth

import "time"

// ExpiresAt is when GitHub expires the token.
func (t Token) ExpiresAt() time.Time { return t.expiresAt }
