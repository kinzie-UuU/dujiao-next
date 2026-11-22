package upstream

import (
	"encoding/json"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
)

const FangfangGmailProductID = "6ae37dc5ed8e0e40e4de28b7"

var gmailTOTP = regexp.MustCompile(`^[A-Z2-7]{16,64}$`)
var errGmailAccount = errors.New("invalid Fang Fang Gmail account")

// FormatFangfangGmailAccount validates both documented account fields and the
// supplier's packed user response. It returns private delivery text and a
// normalized duplicate key; callers must never log either value.
func FormatFangfangGmailAccount(raw json.RawMessage) (string, string, error) {
	var a struct {
		User        string `json:"user"`
		Password    string `json:"password"`
		VerifyEmail string `json:"verifyEmail"`
		OtherInfo   string `json:"otherInfo"`
		ExpiryText  string `json:"expiryText"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return "", "", errGmailAccount
	}
	for _, value := range []string{a.User, a.Password, a.VerifyEmail, a.OtherInfo, a.ExpiryText} {
		if strings.Contains(value, "----") || strings.ContainsAny(value, "\r\n") || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\t' }) {
			return "", "", errGmailAccount
		}
	}
	user, password := strings.TrimSpace(a.User), strings.TrimSpace(a.Password)
	verify, other := strings.TrimSpace(a.VerifyEmail), strings.TrimSpace(a.OtherInfo)
	packed := strings.Contains(user, "|")
	if packed {
		// Reject ambiguous mixtures rather than silently preferring one credential.
		if password != "" || verify != "" || other != "" || strings.TrimSpace(a.ExpiryText) != "" {
			return "", "", errGmailAccount
		}
		fields := strings.Split(user, "|")
		if len(fields) != 4 {
			return "", "", errGmailAccount
		}
		user, password, verify, other = strings.TrimSpace(fields[0]), fields[1], strings.TrimSpace(fields[2]), fields[3]
	}
	address, err := mail.ParseAddress(user)
	if err != nil || address.Address != user || !strings.HasSuffix(strings.ToLower(user), "@gmail.com") || strings.TrimSpace(password) == "" || strings.ContainsAny(user+password, "\t") {
		return "", "", errGmailAccount
	}
	key := strings.ToLower(user)
	if packed {
		// Packed field 3 is a recovery address or TOTP; field 4 starts with TOTP
		// and can contain regional notes separated by spaces or tabs.
		if !gmailRecoveryOrTOTP(verify) {
			return "", "", errGmailAccount
		}
		extra := strings.Fields(other)
		if len(extra) == 0 || !gmailTOTP.MatchString(strings.ToUpper(extra[0])) {
			return "", "", errGmailAccount
		}
		extra[0] = strings.ToUpper(extra[0])
		return user + "----" + password + "----" + verify + "----" + strings.Join(extra, " "), key, nil
	}
	twoFA := strings.ToUpper(strings.ReplaceAll(other, " ", ""))
	if twoFA != "" && !gmailTOTP.MatchString(twoFA) {
		return "", "", errGmailAccount
	}
	if verify != "" {
		if address, err := mail.ParseAddress(verify); err != nil || address.Address != verify {
			candidate := strings.ToUpper(strings.ReplaceAll(verify, " ", ""))
			if !gmailTOTP.MatchString(candidate) || (twoFA != "" && twoFA != candidate) {
				return "", "", errGmailAccount
			}
			twoFA, verify = candidate, ""
		}
	}
	if verify == "" && twoFA == "" {
		return "", "", errGmailAccount
	}
	line := user + "----" + password + "----" + verify
	if twoFA != "" {
		line += "----" + twoFA
	}
	return line, key, nil
}

func gmailRecoveryOrTOTP(value string) bool {
	if address, err := mail.ParseAddress(value); err == nil && address.Address == value {
		return true
	}
	return gmailTOTP.MatchString(strings.ToUpper(strings.ReplaceAll(value, " ", "")))
}
