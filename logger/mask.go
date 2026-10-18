package logger

import "strings"

// MaskSecret keeps only the head and tail of a secret so it can be logged safely
func MaskSecret(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

// MaskDSN hides the password in a database connection string like user:pass@tcp(host)/db
func MaskDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	colon := strings.Index(dsn[:at], ":")
	if colon < 0 {
		return dsn
	}
	return dsn[:colon+1] + "****" + dsn[at:]
}
