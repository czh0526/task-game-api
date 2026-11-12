// Package id generates prefixed identifiers and checks id shapes used by the API.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

var (
	userID   = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,128}$`)
	taskID   = regexp.MustCompile(`^tsk_[A-Za-z0-9]+$`)
	chatID   = regexp.MustCompile(`^cht_[A-Za-z0-9]+$`)
	inviteID = regexp.MustCompile(`^inv_[A-Za-z0-9]+$`)
	refID    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// New returns prefix plus 24 hex characters from crypto/rand.
// A failure from the system RNG means the process cannot continue safely.
func New(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

func ValidUserID(s string) bool   { return userID.MatchString(s) }
func ValidTaskID(s string) bool   { return taskID.MatchString(s) }
func ValidChatID(s string) bool   { return chatID.MatchString(s) }
func ValidInviteID(s string) bool { return inviteID.MatchString(s) }
func ValidRefID(s string) bool    { return refID.MatchString(s) }
